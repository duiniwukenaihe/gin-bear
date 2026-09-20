package agent

import (
	"context"
	"fmt"
	"sync"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// FakeChatModel is the deterministic stand-in for unit tests, evals, and
// offline acceptance. It implements the eino chat surface without any network.
type FakeChatModel struct {
	mu      sync.Mutex
	script  []*schema.Message
	calls   int
	failOn  map[int]error
	usage   []int64
	streams [][]*schema.Message
}

// FakeOption configures the fake.
type FakeOption func(*FakeChatModel)

// WithScript queues assistant answers in order. When exhausted, the fake
// answers with plain text.
func WithScript(messages ...*schema.Message) FakeOption {
	return func(f *FakeChatModel) { f.script = append(f.script, messages...) }
}

// WithFailures makes Generate fail on the given 0-based turn indexes.
func WithFailures(indexes ...int) FakeOption {
	return func(f *FakeChatModel) {
		if f.failOn == nil {
			f.failOn = map[int]error{}
		}
		for _, index := range indexes {
			f.failOn[index] = fmt.Errorf("fake vendor failure at turn %d", index)
		}
	}
}

// WithUsage attaches token reports per turn. Turns without a report exercise
// the conservative reservation path.
func WithUsage(tokens ...int64) FakeOption {
	return func(f *FakeChatModel) { f.usage = append(f.usage, tokens...) }
}

// WithStreams queues chunk sequences for Stream calls.
func WithStreams(chunks ...[]*schema.Message) FakeOption {
	return func(f *FakeChatModel) { f.streams = append(f.streams, chunks...) }
}

// NewFakeChatModel builds a deterministic fake. No network, no key.
func NewFakeChatModel(opts ...FakeOption) *FakeChatModel {
	fake := &FakeChatModel{}
	for _, opt := range opts {
		opt(fake)
	}
	return fake
}

var _ einomodel.BaseChatModel = (*FakeChatModel)(nil)

// Calls reports how many turns the fake served.
func (f *FakeChatModel) Calls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.calls
}

// Generate serves the next scripted answer.
func (f *FakeChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.Message, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if err, ok := f.failOn[f.calls]; ok {
		f.calls++
		return nil, err
	}
	answer := &schema.Message{Role: schema.Assistant, Content: "fake answer"}
	if f.calls < len(f.script) && f.script[f.calls] != nil {
		answer = f.script[f.calls]
	}
	if f.calls < len(f.usage) && f.usage[f.calls] > 0 {
		answer = cloneMessage(answer)
		answer.ResponseMeta = &schema.ResponseMeta{Usage: &schema.TokenUsage{TotalTokens: int(f.usage[f.calls])}}
	}
	f.calls++
	return answer, nil
}

// Stream serves the next scripted chunk sequence.
func (f *FakeChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	chunks := []*schema.Message{{Role: schema.Assistant, Content: "fake stream"}}
	if len(f.streams) > 0 {
		chunks = f.streams[0]
		f.streams = f.streams[1:]
	}
	f.calls++
	return schema.StreamReaderFromArray(chunks), nil
}

func cloneMessage(message *schema.Message) *schema.Message {
	if message == nil {
		return nil
	}
	cloned := *message
	cloned.ToolCalls = append([]schema.ToolCall(nil), message.ToolCalls...)
	return &cloned
}

// FakeText builds a plain assistant answer.
func FakeText(content string) *schema.Message {
	return &schema.Message{Role: schema.Assistant, Content: content}
}

// FakeToolCall builds an assistant answer proposing one tool call.
func FakeToolCall(id, name, args string) *schema.Message {
	return &schema.Message{
		Role: schema.Assistant,
		ToolCalls: []schema.ToolCall{{
			ID:       id,
			Type:     "function",
			Function: schema.FunctionCall{Name: name, Arguments: args},
		}},
	}
}
