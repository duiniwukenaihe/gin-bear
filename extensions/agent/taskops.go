package agent

import (
	"net/http"

	"github.com/duiniwukenaihe/gin-bear/extensions/agent/tasks"
	"github.com/gin-gonic/gin"
)

// Task reconciliation endpoints. They expose the persistent task store over
// HTTP so operators can approve and reconcile without process access:
//
//	GET  /agent/tasks/:id                  task metadata (no args)
//	POST /agent/tasks/:id/approve          consume the approval, queue the task
//	POST /agent/tasks/:id/confirm-unknown  reconcile an unconfirmed write
//	POST /agent/tasks/:id/requeue-unknown  refund and demand a fresh approval
//
// Every endpoint requires trusted identity and enforces tenant isolation: a
// task from another tenant is indistinguishable from a missing one (404).
// Handler.Tasks == nil disables all four with 501.
func (h *Handler) taskStore() (*tasks.Store, bool) {
	if h.Tasks == nil {
		return nil, false
	}
	return h.Tasks, true
}

type taskSummary struct {
	ID         string `json:"id"`
	TenantID   string `json:"tenant_id"`
	Kind       string `json:"kind"`
	Tool       string `json:"tool"`
	Status     string `json:"status"`
	Version    int64  `json:"version"`
	ApprovalID string `json:"approval_id,omitempty"`
	Result     string `json:"result,omitempty"`
	Error      string `json:"error,omitempty"`
	Attempts   int    `json:"attempts"`
}

func summarizeTask(task *tasks.Task) taskSummary {
	return taskSummary{
		ID: task.ID, TenantID: task.TenantID, Kind: task.Kind, Tool: task.Tool,
		Status: task.Status, Version: task.Version, ApprovalID: task.ApprovalID,
		Result: task.Result, Error: task.ErrorText, Attempts: task.Attempts,
	}
}

func (h *Handler) auditTask(identity Identity, decision, outcome, approval string) {
	if h.Auditor == nil {
		return
	}
	h.Auditor.Append(AuditRecord{
		Actor: identity.UserID, Tenant: identity.TenantID,
		Decision: decision, Outcome: outcome, Approval: approval,
	})
}

// loadOwnTask fetches the task and enforces tenant isolation.
func (h *Handler) loadOwnTask(ctx *gin.Context, identity Identity) (*tasks.Task, bool) {
	store, ok := h.taskStore()
	if !ok {
		ctx.JSON(http.StatusNotImplemented, gin.H{"error": "task store is not configured"})
		return nil, false
	}
	task, err := store.Get(ctx.Request.Context(), ctx.Param("id"))
	if err != nil || task.TenantID != identity.TenantID {
		h.auditTask(identity, "task:load", "not_found", "")
		ctx.JSON(http.StatusNotFound, gin.H{"error": "task not found"})
		return nil, false
	}
	return task, true
}

// TaskStatus serves GET /agent/tasks/:id.
func (h *Handler) TaskStatus(ctx *gin.Context) {
	identity, err := h.identity(ctx)
	if err != nil {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "missing trusted identity"})
		return
	}
	task, ok := h.loadOwnTask(ctx, identity)
	if !ok {
		return
	}
	ctx.JSON(http.StatusOK, summarizeTask(task))
}

type approveRequest struct {
	// Args defaults to the submitted args; an explicit value must hash
	// identically or approval refuses (parameter binding).
	Args string `json:"args"`
}

// TaskApprove serves POST /agent/tasks/:id/approve.
func (h *Handler) TaskApprove(ctx *gin.Context) {
	identity, err := h.identity(ctx)
	if err != nil {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "missing trusted identity"})
		return
	}
	task, ok := h.loadOwnTask(ctx, identity)
	if !ok {
		return
	}
	var request approveRequest
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON body"})
		return
	}
	args := request.Args
	if args == "" {
		args = task.Args
	}
	store, _ := h.taskStore()
	approved, err := store.Approve(ctx.Request.Context(), task.ApprovalID, identity.UserID, args)
	if err != nil {
		h.auditTask(identity, "task:approve", "denied", task.ApprovalID)
		ctx.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	h.auditTask(identity, "task:approve", "ok", approved.ApprovalID)
	ctx.JSON(http.StatusOK, summarizeTask(approved))
}

type confirmUnknownRequest struct {
	Result string `json:"result"`
	Usage  int64  `json:"usage"`
}

// TaskConfirmUnknown serves POST /agent/tasks/:id/confirm-unknown.
func (h *Handler) TaskConfirmUnknown(ctx *gin.Context) {
	identity, err := h.identity(ctx)
	if err != nil {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "missing trusted identity"})
		return
	}
	task, ok := h.loadOwnTask(ctx, identity)
	if !ok {
		return
	}
	var request confirmUnknownRequest
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "invalid JSON body"})
		return
	}
	store, _ := h.taskStore()
	confirmed, err := store.ConfirmUnknown(ctx.Request.Context(), task.ID, request.Result, request.Usage)
	if err != nil {
		h.auditTask(identity, "task:confirm-unknown", "denied", task.ApprovalID)
		ctx.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	h.auditTask(identity, "task:confirm-unknown", "ok", confirmed.ApprovalID)
	ctx.JSON(http.StatusOK, summarizeTask(confirmed))
}

// TaskRequeueUnknown serves POST /agent/tasks/:id/requeue-unknown.
func (h *Handler) TaskRequeueUnknown(ctx *gin.Context) {
	identity, err := h.identity(ctx)
	if err != nil {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "missing trusted identity"})
		return
	}
	task, ok := h.loadOwnTask(ctx, identity)
	if !ok {
		return
	}
	store, _ := h.taskStore()
	requeued, err := store.RequeueUnknown(ctx.Request.Context(), task.ID)
	if err != nil {
		h.auditTask(identity, "task:requeue-unknown", "denied", task.ApprovalID)
		ctx.JSON(http.StatusConflict, gin.H{"error": err.Error()})
		return
	}
	h.auditTask(identity, "task:requeue-unknown", "ok", requeued.ApprovalID)
	ctx.JSON(http.StatusOK, summarizeTask(requeued))
}
