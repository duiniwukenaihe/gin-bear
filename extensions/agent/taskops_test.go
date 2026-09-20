package agent_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/duiniwukenaihe/gin-bear/extensions/agent"
	"github.com/duiniwukenaihe/gin-bear/extensions/agent/tasks"
	"github.com/gin-gonic/gin"
	_ "github.com/glebarez/sqlite"
)

func taskOpsApp(t *testing.T, store *tasks.Store, user, tenant string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	handler := &agent.Handler{Tasks: store, Auditor: agent.NewAuditor(100)}
	engine := gin.New()
	if user != "" {
		engine.Use(func(ctx *gin.Context) {
			agent.SetIdentity(ctx, agent.Identity{UserID: user, TenantID: tenant})
		})
	}
	engine.GET("/agent/tasks/:id", handler.TaskStatus)
	engine.POST("/agent/tasks/:id/approve", handler.TaskApprove)
	engine.POST("/agent/tasks/:id/confirm-unknown", handler.TaskConfirmUnknown)
	engine.POST("/agent/tasks/:id/requeue-unknown", handler.TaskRequeueUnknown)
	return engine
}

func openTaskOpsStore(t *testing.T) *tasks.Store {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "ops.db"))
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = db.Close() })
	up, err := tasks.MigrationUp("sqlite")
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range strings.Split(up, ";") {
		if strings.TrimSpace(stmt) == "" {
			continue
		}
		if _, err := db.Exec(stmt); err != nil {
			t.Fatalf("migrate: %v", err)
		}
	}
	store, err := tasks.Open(db, "sqlite")
	if err != nil {
		t.Fatal(err)
	}
	return store
}

func submitOpsWrite(t *testing.T, store *tasks.Store, key string) *tasks.Task {
	t.Helper()
	task, err := store.Submit(context.Background(), tasks.Task{
		TenantID: "t1", Owner: "u1", IdempotencyKey: key,
		Kind: "write", Tool: "update_order", Args: `{"order_id":"1"}`,
		BudgetTokens: 1000,
	})
	if err != nil {
		t.Fatal(err)
	}
	return task
}

func callOps(t *testing.T, app *gin.Engine, method, path, body string) (int, map[string]any) {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	request := httptest.NewRequest(method, path, reader)
	if body != "" {
		request.Header.Set("Content-Type", "application/json")
	}
	recorder := httptest.NewRecorder()
	app.ServeHTTP(recorder, request)
	var decoded map[string]any
	if err := json.Unmarshal(recorder.Body.Bytes(), &decoded); err != nil {
		t.Fatalf("%s %s body = %q", method, path, recorder.Body.String())
	}
	return recorder.Code, decoded
}

// TestTaskOpsApproveFlow is the write-entry ops acceptance: status shows
// metadata without args, approve queues, double approve conflicts, and
// cross-tenant plus missing identity are rejected.
func TestTaskOpsApproveFlow(t *testing.T) {
	store := openTaskOpsStore(t)
	task := submitOpsWrite(t, store, "ops-1")
	app := taskOpsApp(t, store, "op", "t1")

	code, body := callOps(t, app, http.MethodGet, "/agent/tasks/"+task.ID, "")
	if code != http.StatusOK || body["status"] != "waiting_approval" {
		t.Fatalf("status = %d %v, want 200 waiting_approval", code, body)
	}
	if _, leaked := body["args"]; leaked {
		t.Fatalf("status leaks args: %v", body)
	}

	code, body = callOps(t, app, http.MethodPost, "/agent/tasks/"+task.ID+"/approve", `{}`)
	if code != http.StatusOK || body["status"] != "queued" {
		t.Fatalf("approve = %d %v, want 200 queued", code, body)
	}
	code, _ = callOps(t, app, http.MethodPost, "/agent/tasks/"+task.ID+"/approve", `{}`)
	if code != http.StatusConflict {
		t.Fatalf("double approve = %d, want 409", code)
	}

	other := taskOpsApp(t, store, "mallory", "t2")
	code, _ = callOps(t, other, http.MethodGet, "/agent/tasks/"+task.ID, "")
	if code != http.StatusNotFound {
		t.Fatalf("cross-tenant status = %d, want 404", code)
	}
	code, _ = callOps(t, other, http.MethodPost, "/agent/tasks/"+task.ID+"/approve", `{}`)
	if code != http.StatusNotFound {
		t.Fatalf("cross-tenant approve = %d, want 404", code)
	}

	anonymous := taskOpsApp(t, nil, "", "")
	code, _ = callOps(t, anonymous, http.MethodGet, "/agent/tasks/"+task.ID, "")
	if code != http.StatusUnauthorized {
		t.Fatalf("anonymous status = %d, want 401", code)
	}

	nostore := taskOpsApp(t, nil, "op", "t1")
	code, _ = callOps(t, nostore, http.MethodGet, "/agent/tasks/"+task.ID, "")
	if code != http.StatusNotImplemented {
		t.Fatalf("no-store status = %d, want 501", code)
	}
}

// driveToUnknown runs the crash path without wall-clock tricks: claim with
// an already-expired lease, record intent (the write may have executed),
// then recover into unknown.
func driveToUnknown(t *testing.T, store *tasks.Store, id string) {
	t.Helper()
	ctx := context.Background()
	claimed, err := store.Claim(ctx, "w1", -time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if claimed.ID != id {
		t.Fatalf("claimed %s, want %s", claimed.ID, id)
	}
	if _, err := store.RecordIntent(ctx, claimed.ID, claimed.Version); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Recover(ctx); err != nil {
		t.Fatal(err)
	}
}

// TestTaskOpsReconcileFlow drives unknown → confirm and unknown → requeue
// through HTTP, proving operators can close the loop without process access.
func TestTaskOpsReconcileFlow(t *testing.T) {
	store := openTaskOpsStore(t)
	ctx := context.Background()
	confirmTask := submitOpsWrite(t, store, "ops-confirm")
	app := taskOpsApp(t, store, "op", "t1")
	if code, _ := callOps(t, app, http.MethodPost, "/agent/tasks/"+confirmTask.ID+"/approve", `{}`); code != http.StatusOK {
		t.Fatalf("approve = %d", code)
	}
	driveToUnknown(t, store, confirmTask.ID)
	code, body := callOps(t, app, http.MethodPost, "/agent/tasks/"+confirmTask.ID+"/confirm-unknown", `{"result":"wrote","usage":60}`)
	if code != http.StatusOK || body["status"] != "succeeded" {
		t.Fatalf("confirm = %d %v, want 200 succeeded", code, body)
	}

	requeueTask := submitOpsWrite(t, store, "ops-requeue")
	if code, _ := callOps(t, app, http.MethodPost, "/agent/tasks/"+requeueTask.ID+"/approve", `{}`); code != http.StatusOK {
		t.Fatalf("approve = %d", code)
	}
	driveToUnknown(t, store, requeueTask.ID)
	oldApproval := ""
	if got, err := store.Get(ctx, requeueTask.ID); err != nil {
		t.Fatal(err)
	} else {
		oldApproval = got.ApprovalID
	}
	code, body = callOps(t, app, http.MethodPost, "/agent/tasks/"+requeueTask.ID+"/requeue-unknown", "")
	if code != http.StatusOK || body["status"] != "waiting_approval" || body["approval_id"] == oldApproval {
		t.Fatalf("requeue = %d %v, want 200 waiting_approval with a fresh approval", code, body)
	}
}
