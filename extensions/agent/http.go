package agent

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/duiniwukenaihe/gin-bear/extensions/agent/tasks"
	"github.com/gin-gonic/gin"
)

// Wire-visible SSE contract. Exactly one terminal event per stream: done or
// error. Deltas are data; clients must not treat them as authorization.
const (
	SSEEventDelta = "delta"
	SSEEventDone  = "done"
	SSEEventError = "error"
)

// Stream limits: bounded buffer, slow-client timeout, overall stream timeout.
const (
	streamBufferEvents = 64
	streamWriteTimeout = 10 * time.Second
)

// InvokeRequest is the strict JSON body for both endpoints.
type InvokeRequest struct {
	// Input is the user text. Model-side tenant/user fields are ignored.
	Input string `json:"input"`
	// Task selects routing. Unknown tasks are rejected.
	Task string `json:"task"`
	// Stream selects the endpoint behavior; the SSE path ignores it.
	Stream bool `json:"stream,omitempty"`
}

// Handler serves one-task agent invocations over a *bear.Bear compatible
// router. Identity must already sit in the gin context (trusted middleware);
// requests without it are rejected before any model call.
type Handler struct {
	Router  *Router
	Tools   *Registry
	Auditor *Auditor
	Metrics *Metrics
	// Tasks wires the persistent task store for the reconciliation
	// endpoints (status/approve/confirm-unknown/requeue-unknown). Nil
	// disables them; invocation paths never need it.
	Tasks *tasks.Store
	// Disabled is the kill switch: when true, every invocation refuses
	// without touching the model or tools.
	Disabled bool

	gatesMu sync.Mutex
	gates   map[string]*Gate
}

func (h *Handler) runnerFor(task string) (*Runner, error) {
	if h.Disabled {
		return nil, fmt.Errorf("agent is disabled")
	}
	if h.Router == nil || h.Tools == nil {
		return nil, fmt.Errorf("agent is not configured")
	}
	route, err := h.Router.Resolve(task)
	if err != nil {
		return nil, err
	}
	h.gatesMu.Lock()
	defer h.gatesMu.Unlock()
	if h.gates == nil {
		h.gates = map[string]*Gate{}
	}
	gate, ok := h.gates[task]
	if !ok {
		gate = NewGate(route.Budget)
		h.gates[task] = gate
	}
	runner, err := h.Router.RunnerFor(task, h.Tools)
	if err != nil {
		return nil, err
	}
	runner.Gate = gate
	return runner, nil
}

func (h *Handler) identity(ctx *gin.Context) (Identity, error) {
	value, exists := ctx.Get(identityGinKey)
	if !exists {
		return Identity{}, fmt.Errorf("missing trusted identity")
	}
	identity, ok := value.(Identity)
	if !ok || identity.UserID == "" {
		return Identity{}, fmt.Errorf("missing trusted identity")
	}
	return identity, nil
}

// Invoke serves POST /agent/invoke with a single JSON terminal result.
func (h *Handler) Invoke(ctx *gin.Context) {
	identity, err := h.identity(ctx)
	if err != nil {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	var request InvokeRequest
	if err := ctx.ShouldBindJSON(&request); err != nil {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	if len(request.Input) > 8192 {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "input too large"})
		return
	}
	runner, err := h.runnerFor(request.Task)
	if err != nil {
		ctx.JSON(http.StatusServiceUnavailable, gin.H{"error": "unavailable"})
		return
	}
	messages := []*schema.Message{{Role: schema.User, Content: request.Input}}
	result, err := runner.Run(requestContext(ctx), identity, messages)
	h.observe(runner, identity, request.Task, result, err)
	if err != nil {
		ctx.JSON(http.StatusInternalServerError, gin.H{"error": "run failed"})
		return
	}
	ctx.JSON(http.StatusOK, gin.H{
		"text":       result.Text,
		"rounds":     result.Rounds,
		"tool_calls": result.ToolCalls,
		"budget_end": result.BudgetEnd,
		"terminated": result.Terminated,
	})
}

// Stream serves GET /agent/stream?task=&input= as SSE. Client disconnect
// cancels the run; already-completed tool effects are not undone.
func (h *Handler) Stream(ctx *gin.Context) {
	identity, err := h.identity(ctx)
	if err != nil {
		ctx.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}
	task := strings.TrimSpace(ctx.Query("task"))
	input := ctx.Query("input")
	if task == "" || len(input) > 8192 {
		ctx.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}
	runner, err := h.runnerFor(task)
	if err != nil {
		ctx.JSON(http.StatusServiceUnavailable, gin.H{"error": "unavailable"})
		return
	}
	ctx.Header("Content-Type", "text/event-stream")
	ctx.Header("Cache-Control", "no-cache")
	ctx.Header("Connection", "keep-alive")
	ctx.Status(http.StatusOK)
	if _, ok := ctx.Writer.(http.Flusher); !ok {
		writeSSEError(ctx, "streaming unsupported")
		return
	}

	runCtx, cancel := context.WithCancel(requestContext(ctx))
	defer cancel()
	go func() {
		<-ctx.Request.Context().Done()
		cancel()
	}()

	events := make(chan sseEvent, streamBufferEvents)
	terminal := make(chan runOutcome, 1)
	go func() {
		result, runErr := runner.RunStream(runCtx, identity, []*schema.Message{{Role: schema.User, Content: input}}, func(delta string) {
			select {
			case events <- sseEvent{kind: SSEEventDelta, data: delta}:
			case <-runCtx.Done():
			}
		})
		terminal <- runOutcome{result: result, err: runErr}
	}()

	// failWrite aborts on a dead/slow client: the run context is already
	// cancelled by the disconnect watcher or the write deadline below.
	failWrite := func(err error) bool {
		if err == nil {
			return false
		}
		cancel()
		return true
	}
	for {
		select {
		case <-ctx.Request.Context().Done():
			return
		case event := <-events:
			if failWrite(writeSSEEvent(ctx, event.kind, event.data)) {
				return
			}
		case outcome := <-terminal:
			h.observe(runner, identity, task, outcome.result, outcome.err)
			// Drain remaining deltas before the single terminal event.
			drain := true
			for drain {
				select {
				case event := <-events:
					if failWrite(writeSSEEvent(ctx, event.kind, event.data)) {
						return
					}
				default:
					drain = false
				}
			}
			if outcome.err != nil {
				if failWrite(writeSSEEvent(ctx, SSEEventError, "run failed")) {
					return
				}
			} else {
				final, _ := json.Marshal(map[string]any{
					"text":       outcome.result.Text,
					"rounds":     outcome.result.Rounds,
					"tool_calls": outcome.result.ToolCalls,
					"budget_end": outcome.result.BudgetEnd,
				})
				if failWrite(writeSSEEvent(ctx, SSEEventDone, string(final))) {
					return
				}
			}
			return
		case <-time.After(streamWriteTimeout * 6):
			// Backstop for a stalled run; socket writes carry their own
			// deadline below.
			cancel()
			_ = writeSSEEvent(ctx, SSEEventError, "stream timeout")
			return
		}
	}
}

type sseEvent struct {
	kind string
	data string
}

type runOutcome struct {
	result *RunResult
	err    error
}

func writeSSEEvent(ctx *gin.Context, kind, data string) error {
	setSocketWriteDeadline(ctx)
	for _, line := range strings.Split(data, "\n") {
		if _, err := fmt.Fprintf(ctx.Writer, "event: %s\ndata: %s\n\n", kind, line); err != nil {
			return err
		}
	}
	if flusher, ok := ctx.Writer.(http.Flusher); ok {
		flusher.Flush()
	}
	return nil
}

// setSocketWriteDeadline bounds one flush on real connections so a stalled
// client fails the write instead of parking a goroutine. Transports without
// deadline support (including httptest recorders) skip it.
func setSocketWriteDeadline(ctx *gin.Context) {
	controller := http.NewResponseController(ctx.Writer)
	_ = controller.SetWriteDeadline(time.Now().Add(streamWriteTimeout))
}

func writeSSEError(ctx *gin.Context, message string) {
	_ = writeSSEEvent(ctx, SSEEventError, message)
}

// observe records audit and metrics from enforcement points only.
func (h *Handler) observe(runner *Runner, identity Identity, task string, result *RunResult, err error) {
	if h.Metrics != nil {
		h.Metrics.AddModelTurn(runner.Provider)
		if result != nil && result.BudgetEnd {
			h.Metrics.AddBudgetEnd()
		}
		if err != nil && isQuotaBlock(err) {
			h.Metrics.AddQuotaBlock()
		}
	}
	if h.Auditor != nil {
		outcome := "ok"
		if err != nil {
			outcome = "error"
		} else if result != nil && result.BudgetEnd {
			outcome = "budget_end"
		}
		h.Auditor.Append(AuditRecord{
			Actor:    identity.UserID,
			Tenant:   identity.TenantID,
			Decision: "invoke:" + task,
			Outcome:  outcome,
			Provider: runner.Provider,
		})
	}
}

func isQuotaBlock(err error) bool {
	message := err.Error()
	return strings.Contains(message, "quota exhausted") || strings.Contains(message, "too many concurrent")
}

// RunStream mirrors Run but forwards model text deltas. Cancellation stops
// new turns; the terminal outcome still reports rounds and calls served.
func (r *Runner) RunStream(ctx context.Context, identity Identity, input []*schema.Message, onDelta func(string)) (*RunResult, error) {
	ledger, release, err := r.admit(ctx, identity)
	if err != nil {
		return nil, err
	}
	defer release()
	ctx = WithIdentity(ctx, identity)
	ctx, cancel := context.WithTimeout(ctx, r.Budget.TotalTimeout)
	defer cancel()

	messages := append([]*schema.Message(nil), input...)
	result := &RunResult{}
	for round := 0; round < r.Budget.MaxRounds; round++ {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		allowance, err := streamAllowance(r.Budget, ledger, messages)
		if err != nil {
			result.BudgetEnd, result.Terminated = true, err.Error()
			return result, nil
		}
		stream, err := r.Model.Stream(ctx, messages, einomodel.WithTools(r.Tools.ToolInfos()), einomodel.WithMaxTokens(allowance))
		if err != nil {
			return result, fmt.Errorf("model stream failed: %w", err)
		}
		answer, streamErr := collectStream(stream, onDelta)
		if streamErr != nil {
			return result, streamErr
		}
		if usage, ok := modelUsage(answer); ok {
			if err := ledger.chargeTokens(usage, true); err != nil {
				result.BudgetEnd, result.Terminated = true, "token budget exhausted"
				return result, nil
			}
		} else if err := ledger.chargeTokens(0, false); err != nil {
			result.BudgetEnd, result.Terminated = true, "token budget exhausted"
			return result, nil
		}
		result.Rounds = round + 1
		if len(answer.ToolCalls) == 0 {
			result.Text = answer.Content
			return result, nil
		}
		messages = append(messages, answer)
		for _, call := range answer.ToolCalls {
			toolMessage, executed, toolErr := r.invoke(ctx, ledger, identity, call)
			messages = append(messages, toolMessage)
			if executed {
				result.ToolCalls++
			}
			if toolErr != nil && (isBudgetEnd(toolErr) || isCancel(toolErr)) {
				result.BudgetEnd = isBudgetEnd(toolErr)
				result.Terminated = toolErr.Error()
				return result, nil
			}
		}
	}
	result.BudgetEnd, result.Terminated = true, "max rounds reached"
	return result, nil
}

// collectStream merges chunks, forwarding text deltas as they arrive.
func collectStream(stream *schema.StreamReader[*schema.Message], onDelta func(string)) (*schema.Message, error) {
	defer stream.Close()
	merged := &schema.Message{Role: schema.Assistant}
	var calls []schema.ToolCall
	byID := map[string]int{}
	for {
		chunk, err := stream.Recv()
		if err != nil {
			if isEOF(err) {
				break
			}
			return nil, fmt.Errorf("model stream failed: %w", err)
		}
		if chunk == nil {
			continue
		}
		if chunk.Content != "" {
			merged.Content += chunk.Content
			if onDelta != nil {
				onDelta(chunk.Content)
			}
		}
		for _, call := range chunk.ToolCalls {
			if at, ok := byID[call.ID]; ok && call.ID != "" {
				calls[at].Function.Name += call.Function.Name
				calls[at].Function.Arguments += call.Function.Arguments
				continue
			}
			byID[call.ID] = len(calls)
			calls = append(calls, call)
		}
		if chunk.ResponseMeta != nil {
			merged.ResponseMeta = chunk.ResponseMeta
		}
	}
	merged.ToolCalls = calls
	return merged, nil
}

func isEOF(err error) bool {
	return errors.Is(err, io.EOF)
}

// requestContext prefers the HTTP request context and never Backgrounds a
// live request.
func requestContext(ctx *gin.Context) context.Context {
	if ctx != nil && ctx.Request != nil && ctx.Request.Context() != nil {
		return ctx.Request.Context()
	}
	return context.Background()
}

// identityGinKey carries trusted identity in gin context. Only server-side
// middleware may set it.
const identityGinKey = "bear_agent_identity"

// SetIdentity stores trusted identity for handlers (middleware use only).
func SetIdentity(ctx *gin.Context, identity Identity) {
	ctx.Set(identityGinKey, identity)
}
