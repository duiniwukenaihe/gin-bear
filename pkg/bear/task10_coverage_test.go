package bear

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/redis/go-redis/v9"
)

type Task10EmbeddedBinding struct {
	Embedded string `form:"embedded"`
}

type task10ScalarBinding struct {
	Task10EmbeddedBinding
	Text     string  `form:"text"`
	Enabled  bool    `form:"enabled"`
	Signed   int16   `form:"signed"`
	Unsigned uint16  `form:"unsigned"`
	Ratio    float32 `form:"ratio"`
	Pointer  *int    `form:"pointer"`
	Skipped  string  `form:"-"`
	private  string  `form:"private"`
}

type task10ReadError struct{}

func (task10ReadError) Read([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestTask10BindingCoversScalarPointerAndEmbeddedFields(t *testing.T) {
	var target task10ScalarBinding
	values := map[string][]string{
		"embedded": {"inside"},
		"text":     {"value"},
		"enabled":  {"true"},
		"signed":   {"-12"},
		"unsigned": {"42"},
		"ratio":    {"1.5"},
		"pointer":  {"7"},
		"private":  {"hidden"},
	}
	if err := bindTaggedFields(reflect.ValueOf(&target), values, "form"); err != nil {
		t.Fatal(err)
	}
	if target.Embedded != "inside" || target.Text != "value" || !target.Enabled || target.Signed != -12 || target.Unsigned != 42 || target.Ratio != 1.5 {
		t.Fatalf("scalar binding = %#v", target)
	}
	if target.Pointer == nil || *target.Pointer != 7 {
		t.Fatalf("pointer binding = %#v", target.Pointer)
	}
	if target.Skipped != "" || target.private != "" {
		t.Fatalf("ignored fields were bound: %#v", target)
	}

	for _, tt := range []struct {
		name  string
		field interface{}
		raw   string
	}{
		{name: "bool", field: new(bool), raw: "truthy"},
		{name: "int", field: new(int8), raw: "128"},
		{name: "uint", field: new(uint8), raw: "-1"},
		{name: "float", field: new(float32), raw: "many"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			value := reflect.ValueOf(tt.field).Elem()
			if err := setFieldValue(value, tt.raw); err == nil {
				t.Fatalf("setFieldValue(%s) accepted %q", value.Type(), tt.raw)
			}
		})
	}

	var nilTarget *task10ScalarBinding
	if err := bindTaggedFields(reflect.ValueOf(nilTarget), values, "form"); err != nil {
		t.Fatalf("nil target error = %v", err)
	}
	nonStruct := 1
	if err := bindTaggedFields(reflect.ValueOf(&nonStruct), values, "form"); err != nil {
		t.Fatalf("non-struct target error = %v", err)
	}
}

func TestTask10BindingCoversFormJSONAndPathErrors(t *testing.T) {
	recorder := httptest.NewRecorder()
	ctx, _ := gin.CreateTestContext(recorder)
	request := httptest.NewRequest(http.MethodPost, "/items?q=query", strings.NewReader("name=form&count=8"))
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	ctx.Request = request
	var form struct {
		Query string `query:"q"`
		Name  string `form:"name"`
		Count int    `form:"count"`
	}
	if err := bindRequest(ctx, &form); err != nil {
		t.Fatal(err)
	}
	if form.Query != "query" || form.Name != "form" || form.Count != 8 {
		t.Fatalf("form binding = %#v", form)
	}

	ctx.Request = httptest.NewRequest(http.MethodPost, "/items", task10ReadError{})
	ctx.Request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if err := bindRequest(ctx, &form); err == nil {
		t.Fatal("form read error was ignored")
	}

	ctx.Request = httptest.NewRequest(http.MethodPost, "/items", strings.NewReader(""))
	ctx.Request.ContentLength = -1
	ctx.Request.Header.Set("Content-Type", "application/json")
	if err := bindRequest(ctx, &form); !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("empty JSON error = %v", err)
	}
	ctx.Request.Header.Set("Content-Type", "not a media type")
	if isJSONRequest(ctx.Request) || isFormRequest(ctx.Request) {
		t.Fatal("malformed media type was treated as bindable")
	}

	ctx.Params = gin.Params{{Key: "id", Value: "42"}}
	stringValue, err := pathStringBinder(reflect.TypeOf(""), 0)(ctx)
	if err != nil || stringValue.String() != "42" {
		t.Fatalf("string path = %v, err=%v", stringValue, err)
	}
	integerValue, err := pathIntegerBinder(reflect.TypeOf(int16(0)), 0)(ctx)
	if err != nil || integerValue.Int() != 42 {
		t.Fatalf("integer path = %v, err=%v", integerValue, err)
	}
	ctx.Params[0].Value = "many"
	if _, err := pathIntegerBinder(reflect.TypeOf(int8(0)), 0)(ctx); err == nil {
		t.Fatal("invalid integer path was accepted")
	} else {
		var pathErr *pathBindingError
		if !errors.As(err, &pathErr) || pathErr.Unwrap() == nil {
			t.Fatalf("integer path error = %v", err)
		}
	}
	ctx.Params = nil
	if _, err := pathStringBinder(reflect.TypeOf(""), 0)(ctx); err == nil {
		t.Fatal("missing string path was accepted")
	}

	if _, err := compileArguments(reflect.TypeOf(func(struct{}, struct{}) {})); err == nil || !strings.Contains(err.Error(), "more than one request struct") {
		t.Fatalf("duplicate request error = %v", err)
	}
}

func TestTask10AuthTokenRevocationAndBlacklistErrors(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{
		Addr:         server.Addr(),
		MaxRetries:   -1,
		DialTimeout:  500 * time.Millisecond,
		ReadTimeout:  500 * time.Millisecond,
		WriteTimeout: 500 * time.Millisecond,
	})
	t.Cleanup(func() { _ = client.Close() })
	util := NewJWTUtil("task10-secret-1234567890", 1)
	manager := &AuthTokenManager{JWTUtil: util, Redis: &RedisAdapter{Client: client}}

	token, err := manager.GenerateToken(17, "task10@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if err := manager.RevokeToken(context.Background(), token); err != nil {
		t.Fatal(err)
	}
	blacklisted, err := manager.IsTokenBlacklisted(context.Background(), token)
	if err != nil || !blacklisted {
		t.Fatalf("blacklisted = %v, err=%v", blacklisted, err)
	}
	if _, err := manager.ParseToken(token); err == nil || !strings.Contains(err.Error(), "revoked") {
		t.Fatalf("revoked token parse error = %v", err)
	}
	if err := manager.RevokeToken(context.Background(), "not-a-token"); err == nil {
		t.Fatal("invalid token was revoked")
	}

	skewed := NewJWTUtil("task10-secret-1234567890", 1)
	skewed.Config.ClockSkew = time.Minute
	expired := signedJWT(t, skewed.Config.Secret, jwt.RegisteredClaims{ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Second))})
	manager.JWTUtil = skewed
	if err := manager.RevokeToken(context.Background(), expired); err != nil {
		t.Fatalf("already expired token revoke error = %v", err)
	}

	server.Close()
	errorCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err := manager.IsTokenBlacklisted(errorCtx, token); err == nil {
		t.Fatal("closed revocation store returned no error")
	}
	manager.JWTUtil = util
	if _, err := manager.ParseToken(token); err == nil || !strings.Contains(err.Error(), "failed to check token blacklist") {
		t.Fatalf("blacklist lookup error = %v", err)
	}
}

func TestTask10AuthFairingCoversBearerBranches(t *testing.T) {
	util := NewJWTUtil("task10-secret-1234567890", 1)
	token, err := util.GenerateToken(23, "bear@example.com")
	if err != nil {
		t.Fatal(err)
	}

	for _, tt := range []struct {
		name    string
		header  string
		fairing *AuthFairing
		wantErr bool
	}{
		{name: "missing", fairing: NewAuthFairing(), wantErr: true},
		{name: "format", header: "Basic value", fairing: NewAuthFairing(), wantErr: true},
		{name: "unconfigured", header: "Bearer " + token, fairing: NewAuthFairing(), wantErr: true},
		{name: "invalid", header: "Bearer broken", fairing: &AuthFairing{JWTUtil: util}, wantErr: true},
		{name: "jwt", header: "Bearer " + token, fairing: &AuthFairing{JWTUtil: util}},
		{name: "manager", header: "Bearer " + token, fairing: &AuthFairing{TokenManager: &AuthTokenManager{JWTUtil: util}}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
			ctx.Request = httptest.NewRequest(http.MethodGet, "/private", nil)
			if tt.header != "" {
				ctx.Request.Header.Set("Authorization", tt.header)
			}
			err := tt.fairing.OnRequest(ctx)
			if tt.wantErr {
				if err == nil {
					t.Fatal("expected authorization error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if userID, exists := ctx.Get("current_user_id"); !exists || userID != uint(23) {
				t.Fatalf("current user = %#v, exists=%v", userID, exists)
			}
			if currentToken, exists := ctx.Get("current_token"); !exists || currentToken != token {
				t.Fatalf("current token was not retained for revocation")
			}
		})
	}

	if NewAuthFairing().Name() != "AuthFairing" {
		t.Fatal("auth fairing name changed")
	}
	for _, tt := range []struct {
		path    string
		pattern string
		want    bool
	}{
		{path: "/live", pattern: "", want: false},
		{path: "/live", pattern: "/live", want: true},
		{path: "/public", pattern: "/public/*", want: true},
		{path: "/public/item", pattern: "/public/*", want: true},
		{path: "/private", pattern: "/public/*", want: false},
	} {
		if got := publicPathMatch(tt.path, tt.pattern); got != tt.want {
			t.Fatalf("publicPathMatch(%q, %q) = %v", tt.path, tt.pattern, got)
		}
	}
}

func TestTask10MigrationRollbackValidationAndLoadErrors(t *testing.T) {
	if _, err := LoadSQLMigrations(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("missing migration directory was accepted")
	}
	if err := (*MigrationRunner)(nil).Down(context.Background(), nil, 1); err == nil {
		t.Fatal("nil migration runner was accepted")
	}
	db := newMigrationTestDB(t)
	runner := NewMigrationRunner(db)
	if err := runner.Down(context.Background(), nil, 0); err != nil {
		t.Fatalf("zero-step rollback error = %v", err)
	}
	migration := Migration{Version: "001", Name: "users", UpSQL: "CREATE TABLE users (id INTEGER PRIMARY KEY);", DownSQL: "DROP TABLE users;"}
	if err := runner.Up(context.Background(), []Migration{migration}); err != nil {
		t.Fatal(err)
	}
	if err := runner.Down(context.Background(), nil, 1); err == nil || !strings.Contains(err.Error(), "migration file not loaded") {
		t.Fatalf("missing rollback migration error = %v", err)
	}
	withoutDown := migration
	withoutDown.DownSQL = ""
	if err := runner.Down(context.Background(), []Migration{withoutDown}, 1); err == nil || !strings.Contains(err.Error(), "down sql is empty") {
		t.Fatalf("empty rollback SQL error = %v", err)
	}

	for _, name := range []string{"", "1table", "table-name"} {
		if err := validateMigrationTableName(name); err == nil {
			t.Fatalf("unsafe migration table %q was accepted", name)
		}
	}
	if err := validateMigrationTableName("schema_2026"); err != nil {
		t.Fatalf("valid migration table rejected: %v", err)
	}
	runner.LockTable = "unsafe-lock-table"
	if err := runner.ForceUnlock(context.Background()); err == nil || !strings.Contains(err.Error(), "invalid migration table name") {
		t.Fatalf("unsafe force-unlock table error = %v", err)
	}
}

func TestTask10CronLockValidatesDependenciesAndOwner(t *testing.T) {
	if err := (*cronLock)(nil).Acquire(context.Background()); err == nil {
		t.Fatal("nil cron lock acquired")
	}
	if err := (*cronLock)(nil).Release(context.Background()); err == nil {
		t.Fatal("nil cron lock released")
	}
	if err := newCronLock(nil, "jobs:test", "owner", time.Second).Acquire(context.Background()); err == nil {
		t.Fatal("cron lock without Redis acquired")
	}
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	if err := newCronLock(client, "jobs:test", "owner", 0).Acquire(context.Background()); err == nil || !strings.Contains(err.Error(), "TTL must be positive") {
		t.Fatalf("zero TTL error = %v", err)
	}
	lock, err := newOwnedCronLock(client, "jobs:owned", time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	if len(lock.owner) != 32 || lock.key != "jobs:owned" || lock.ttl != time.Minute {
		t.Fatalf("owned cron lock = %#v", lock)
	}
}
