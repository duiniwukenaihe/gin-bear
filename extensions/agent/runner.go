package agent

import (
	"context"
	"errors"
	"fmt"
	"strings"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// Usage is the token spend one model turn reports. HasReport distinguishes a
// real vendor report from silence (which the ledger charges conservatively).
type Usage struct {
	Tokens    int64
	HasReport bool
}

// ChatModel is the eino chat surface the runner drives: Generate for plain
// turns, Stream for SSE turns. Fake and vendor adapters both implement it.
type ChatModel interface {
	einomodel.BaseChatModel
}

// RunResult is the terminal outcome of one run. Exactly one terminal state
// exists per run: text, error, or budget end.
type RunResult struct {
	Text       string
	Rounds     int
	ToolCalls  int
	BudgetEnd  bool
	Terminated string
}

// Runner executes one single-agent run: propose (model) -> authorize (policy)
// -> execute (tool) -> repeat, under budget. Model output can only propose.
type Runner struct {
	Model    ChatModel
	Tools    *Registry
	Budget   Budget
	Provider string
	// Gate shares admission state across runs. Nil means an isolated gate
	// with no cross-run limits.
	Gate *Gate
}

// errBudgetExhausted ends a run before any further vendor spend.
var errBudgetExhausted = errors.New("token budget exhausted")

// Run drives non-streaming turns until text, error, or budget end.
func (r *Runner) Run(ctx context.Context, identity Identity, input []*schema.Message) (*RunResult, error) {
	ledger, release, err := r.admit(ctx, identity)
	if err != nil {
		return nil, err
	}
	defer release()
	// One trusted binding for the whole run: Authorize and Execute observe
	// the same identity, whether the run arrived over HTTP or not.
	ctx = WithIdentity(ctx, identity)
	ctx, cancel := context.WithTimeout(ctx, r.Budget.TotalTimeout)
	defer cancel()

	messages := append([]*schema.Message(nil), input...)
	result := &RunResult{}
	for round := 0; round < r.Budget.MaxRounds; round++ {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		answer, usage, err := r.turn(ctx, ledger, messages)
		if err != nil {
			if errors.Is(err, errBudgetExhausted) {
				result.BudgetEnd, result.Terminated = true, errBudgetExhausted.Error()
				return result, nil
			}
			return result, err
		}
		if err := ledger.chargeTokens(usage.Tokens, usage.HasReport); err != nil {
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
			if toolErr != nil {
				// Tool failures are data for the model, except budget ends
				// and cancellations which terminate the run.
				if isBudgetEnd(toolErr) || isCancel(toolErr) {
					result.BudgetEnd = isBudgetEnd(toolErr)
					result.Terminated = toolErr.Error()
					return result, nil
				}
			}
		}
	}
	result.BudgetEnd, result.Terminated = true, "max rounds reached"
	return result, nil
}

// turn asks the model once with the whitelisted tools bound per request. It
// refuses before dialing when the remaining budget cannot cover the
// estimated input plus one minimal turn, and caps vendor output to the
// remaining allowance via max_tokens.
func (r *Runner) turn(ctx context.Context, ledger *Ledger, messages []*schema.Message) (*schema.Message, Usage, error) {
	remaining := r.Budget.MaxTokens - ledger.spent()
	if remaining <= 0 {
		return nil, Usage{}, errBudgetExhausted
	}
	inputEstimate := int64(estimateTokens(messages))
	if inputEstimate >= remaining {
		return nil, Usage{}, errBudgetExhausted
	}
	allowance := int64(r.Budget.MaxOutputTokens)
	if allowance > remaining-inputEstimate {
		allowance = remaining - inputEstimate
	}
	if allowance < 1 {
		return nil, Usage{}, errBudgetExhausted
	}
	answer, err := r.Model.Generate(ctx, messages,
		einomodel.WithTools(r.Tools.ToolInfos()),
		einomodel.WithMaxTokens(int(allowance)),
	)
	if err != nil {
		return nil, Usage{}, fmt.Errorf("model turn failed: %w", err)
	}
	usage := Usage{}
	if answer == nil {
		return nil, usage, fmt.Errorf("model returned no message")
	}
	if reported, ok := modelUsage(answer); ok {
		usage = Usage{Tokens: reported, HasReport: true}
	}
	return answer, usage, nil
}

// streamAllowance computes the vendor output cap for one streaming turn,
// refusing before dialing when the remaining budget cannot cover it.
func streamAllowance(budget Budget, ledger *Ledger, messages []*schema.Message) (int, error) {
	remaining := budget.MaxTokens - ledger.spent()
	if remaining <= 0 {
		return 0, errBudgetExhausted
	}
	inputEstimate := int64(estimateTokens(messages))
	if inputEstimate >= remaining {
		return 0, errBudgetExhausted
	}
	allowance := int64(budget.MaxOutputTokens)
	if allowance > remaining-inputEstimate {
		allowance = remaining - inputEstimate
	}
	if allowance < 1 {
		return 0, errBudgetExhausted
	}
	return int(allowance), nil
}

// estimateTokens approximates input size at 4 chars per token plus
// per-message framing. Best-effort input metering only; hard caps apply to
// vendor output (max_tokens) and to counted spend (reports plus reserves).
func estimateTokens(messages []*schema.Message) int {
	total := 0
	for _, message := range messages {
		if message == nil {
			continue
		}
		total += len(message.Content) + 64
		for _, call := range message.ToolCalls {
			total += len(call.ID) + len(call.Function.Name) + len(call.Function.Arguments) + 32
		}
	}
	return total / 4
}

// invoke authorizes then executes one proposed call. Unknown tools,
// oversized args, authorization denials, and budget breaches never execute;
// only real executions count. The trusted identity is bound into the call
// context as well, so Execute observes it without trusting model text.
func (r *Runner) invoke(ctx context.Context, ledger *Ledger, identity Identity, call schema.ToolCall) (*schema.Message, bool, error) {
	ctx = WithIdentity(ctx, identity)
	name := call.Function.Name
	tool, ok := r.Tools.Lookup(name)
	if !ok {
		return toolErrorMessage(call, fmt.Sprintf("unknown tool %q", name)), false, fmt.Errorf("unknown tool %q", name)
	}
	args, err := decodeArgs(call.Function.Arguments, tool.MaxArgsBytes)
	if err != nil {
		return toolErrorMessage(call, err.Error()), false, err
	}
	if err := validateToolArgs(tool.Info, args); err != nil {
		return toolErrorMessage(call, err.Error()), false, err
	}
	if err := tool.Authorize(ctx, identity, args); err != nil {
		return toolErrorMessage(call, "not authorized"), false, err
	}
	if err := ledger.chargeCall(name); err != nil {
		return toolErrorMessage(call, err.Error()), false, err
	}
	toolCtx, cancel := context.WithTimeout(ctx, r.Budget.ToolTimeout)
	defer cancel()
	output, err := tool.Execute(toolCtx, args)
	if err != nil {
		return toolErrorMessage(call, err.Error()), true, err
	}
	if len(output) > tool.MaxArgsBytes*4 {
		output = output[:tool.MaxArgsBytes*4] + "...[truncated]"
	}
	return &schema.Message{
		Role:       schema.Tool,
		Content:    output,
		ToolCallID: call.ID,
		Name:       name,
	}, true, nil
}

func toolErrorMessage(call schema.ToolCall, detail string) *schema.Message {
	return &schema.Message{
		Role:       schema.Tool,
		Content:    "tool error: " + detail,
		ToolCallID: call.ID,
		Name:       call.Function.Name,
	}
}

func (r *Runner) admit(ctx context.Context, identity Identity) (*Ledger, func(), error) {
	if identity.UserID == "" {
		return nil, nil, fmt.Errorf("runs require a trusted identity")
	}
	if r.Model == nil || r.Tools == nil {
		return nil, nil, fmt.Errorf("runner needs a model and a tool registry")
	}
	r.Budget = r.Budget.withDefaults()
	ledger := &Ledger{calls: map[string]int{}, budget: r.Budget}
	gate := r.Gate
	if gate == nil {
		gate = NewGate(r.Budget)
	}
	release, err := gate.beginRun(identity.UserID)
	if err != nil {
		return nil, nil, err
	}
	if err := ctx.Err(); err != nil {
		release()
		return nil, nil, err
	}
	return ledger, release, nil
}

func isBudgetEnd(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "budget exhausted") ||
		strings.Contains(message, "call cap exceeded") ||
		strings.Contains(message, "max rounds")
}

func isCancel(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "context canceled") ||
		strings.Contains(message, "context deadline exceeded")
}

// modelUsage reads vendor token usage when the message carries it.
func modelUsage(answer *schema.Message) (int64, bool) {
	if answer == nil || answer.ResponseMeta == nil || answer.ResponseMeta.Usage == nil {
		return 0, false
	}
	usage := answer.ResponseMeta.Usage
	total := int64(usage.TotalTokens)
	if total <= 0 {
		total = int64(usage.PromptTokens + usage.CompletionTokens)
	}
	if total <= 0 {
		return 0, false
	}
	return total, true
}
