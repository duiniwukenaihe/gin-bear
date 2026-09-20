package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/schema"
)

// Tool is one whitelisted business capability. The model may only propose
// calls; every invocation re-authorizes through Authorize with the trusted
// identity and executes through Execute under budget. Tool outputs are data.
type Tool struct {
	Info *schema.ToolInfo
	// Authorize decides whether identity may invoke with these decoded args.
	// It runs on every call, including retries and resumed runs.
	Authorize func(ctx context.Context, identity Identity, args map[string]any) error
	// Execute performs the read (W7/W8) or approved write (W9). It must not
	// trust model text beyond the validated args.
	Execute func(ctx context.Context, args map[string]any) (string, error)
	// MaxArgsBytes caps the JSON-encoded arguments per call.
	MaxArgsBytes int
}

// Registry is the closed tool whitelist for one runner.
type Registry struct {
	mu    sync.RWMutex
	tools map[string]*Tool
}

// NewRegistry builds a whitelist. Unknown names are rejected at call time;
// duplicate names are rejected at build time. Parameter schemas outside the
// enforceable subset are also rejected at build time, so neither execution
// nor the vendor can silently run against a downgraded contract.
func NewRegistry(tools ...*Tool) (*Registry, error) {
	registry := &Registry{tools: map[string]*Tool{}}
	for _, tool := range tools {
		if tool == nil || tool.Info == nil || strings.TrimSpace(tool.Info.Name) == "" {
			return nil, fmt.Errorf("agent tool needs a name")
		}
		if tool.Execute == nil {
			return nil, fmt.Errorf("agent tool %q needs Execute", tool.Info.Name)
		}
		if _, err := declaredParams(tool.Info); err != nil {
			return nil, fmt.Errorf("agent tool %q has an unsupported parameter schema: %w", tool.Info.Name, err)
		}
		if _, exists := registry.tools[tool.Info.Name]; exists {
			return nil, fmt.Errorf("duplicate agent tool %q", tool.Info.Name)
		}
		if tool.MaxArgsBytes <= 0 {
			tool.MaxArgsBytes = 4096
		}
		registry.tools[tool.Info.Name] = tool
	}
	return registry, nil
}

// Lookup returns the tool and a copy of the whitelist names for the model.
func (r *Registry) Lookup(name string) (*Tool, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	tool, ok := r.tools[name]
	return tool, ok
}

// ToolInfos lists whitelisted tools for model binding.
func (r *Registry) ToolInfos() []*schema.ToolInfo {
	r.mu.RLock()
	defer r.mu.RUnlock()
	infos := make([]*schema.ToolInfo, 0, len(r.tools))
	for _, tool := range r.tools {
		infos = append(infos, tool.Info)
	}
	return infos
}

// Names lists whitelisted tool names, sorted for stable prompts.
func (r *Registry) Names() []string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	names := make([]string, 0, len(r.tools))
	for name := range r.tools {
		names = append(names, name)
	}
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j] < names[j-1]; j-- {
			names[j], names[j-1] = names[j-1], names[j]
		}
	}
	return names
}

// decodeArgs validates size and shape. Unknown tools never reach here;
// oversized or malformed args are rejected before authorization.
func decodeArgs(raw string, maxBytes int) (map[string]any, error) {
	if len(raw) > maxBytes {
		return nil, fmt.Errorf("tool arguments exceed %d bytes", maxBytes)
	}
	args := map[string]any{}
	if strings.TrimSpace(raw) == "" {
		return args, nil
	}
	decoder := json.NewDecoder(strings.NewReader(raw))
	if err := decoder.Decode(&args); err != nil {
		return nil, fmt.Errorf("tool arguments are not a JSON object: %w", err)
	}
	return args, nil
}

// Budget caps one run. Zero values are replaced by conservative defaults;
// every limit is enforced server-side, never by model promise.
//
// Metering contract (see adr-002-budget-metering.md): the ledger counts
// vendor-reported tokens exactly, plus a conservative reserve per silent
// turn. Before each model call the runner refuses when the remaining budget
// cannot cover the estimated input plus one minimal turn, and it passes the
// remaining output allowance to the vendor as max_tokens. Vendor output is
// therefore hard-capped; input metering is a chars/4 heuristic, so input
// spend is bounded best-effort, never claimed as a hard vendor-side guarantee.
type Budget struct {
	MaxRounds       int
	MaxTokens       int64
	ReserveTokens   int64
	MaxOutputTokens int
	TotalTimeout    time.Duration
	ToolTimeout     time.Duration
	MaxCallsPerTool int
	MaxConcurrent   int
	MaxUserCalls    int64
}

func (b Budget) withDefaults() Budget {
	if b.MaxRounds <= 0 {
		b.MaxRounds = 8
	}
	if b.MaxTokens <= 0 {
		b.MaxTokens = 20000
	}
	if b.ReserveTokens <= 0 {
		b.ReserveTokens = 2000
	}
	if b.MaxOutputTokens <= 0 {
		b.MaxOutputTokens = 1024
	}
	if b.TotalTimeout <= 0 {
		b.TotalTimeout = 2 * time.Minute
	}
	if b.ToolTimeout <= 0 {
		b.ToolTimeout = 20 * time.Second
	}
	if b.MaxCallsPerTool <= 0 {
		b.MaxCallsPerTool = 10
	}
	if b.MaxConcurrent <= 0 {
		b.MaxConcurrent = 4
	}
	if b.MaxUserCalls <= 0 {
		b.MaxUserCalls = 100
	}
	return b
}

// Ledger tracks spend against one run's budget. Usage reported by the vendor
// is recorded exactly; when the vendor reports nothing, ReserveTokens is
// charged conservatively instead of zero.
type Ledger struct {
	mu        sync.Mutex
	tokens    int64
	calls     map[string]int
	budget    Budget
	exhausted bool
}

// Gate enforces cross-run admission: per-user concurrency and lifetime call
// quota. One Gate is shared by every run behind a Handler; runs without a
// shared gate get an isolated one (no cross-run limits).
type Gate struct {
	mu        sync.Mutex
	inflight  map[string]int
	userCalls map[string]int64
	budget    Budget
}

// NewGate builds admission state for one budget.
func NewGate(budget Budget) *Gate {
	return &Gate{inflight: map[string]int{}, userCalls: map[string]int64{}, budget: budget.withDefaults()}
}

// beginRun charges admission: concurrency and per-user quota. It returns a
// release function the runner must call.
func (g *Gate) beginRun(user string) (release func(), err error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.inflight[user] >= g.budget.MaxConcurrent {
		return nil, fmt.Errorf("too many concurrent runs for this user")
	}
	if g.userCalls[user] >= g.budget.MaxUserCalls {
		return nil, fmt.Errorf("user call quota exhausted")
	}
	g.inflight[user]++
	g.userCalls[user]++
	return func() {
		g.mu.Lock()
		defer g.mu.Unlock()
		g.inflight[user]--
	}, nil
}

// chargeTokens records vendor usage, or the conservative reserve.
func (l *Ledger) chargeTokens(reported int64, hasReport bool) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	spend := reported
	if !hasReport {
		spend = l.budget.ReserveTokens
	}
	l.tokens += spend
	if l.tokens > l.budget.MaxTokens {
		l.exhausted = true
		return fmt.Errorf("token budget exhausted")
	}
	return nil
}

// chargeCall records one tool invocation against per-tool caps.
func (l *Ledger) chargeCall(tool string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.calls[tool]++
	if l.calls[tool] > l.budget.MaxCallsPerTool {
		return fmt.Errorf("tool %q call cap exceeded", tool)
	}
	return nil
}

// spent reports counted tokens for pre-call admission.
func (l *Ledger) spent() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.tokens
}

// Snapshot reports spend for metrics and audit. No identities beyond the
// owning user, no prompts, no tool outputs.
func (l *Ledger) Snapshot() (tokens int64, calls map[string]int, exhausted bool) {
	l.mu.Lock()
	defer l.mu.Unlock()
	calls = make(map[string]int, len(l.calls))
	for tool, count := range l.calls {
		calls[tool] = count
	}
	return l.tokens, calls, l.exhausted
}
