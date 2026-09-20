package agent

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strings"
	"time"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// Reasoning modes for OpenAI-compatible vendors that emit interleaved
// thinking text in the assistant content. The values are matched
// case- and whitespace-insensitively in OpenAIConfig.stripThinking.
const (
	// ReasoningOff disables any post-processing. The vendor's `content`
	// string is returned verbatim, including any <think> blocks.
	ReasoningOff = "off"
	// ReasoningStrip removes <think>...</think> blocks (and the
	// trailing whitespace delimiter) from assistant content before the
	// adapter returns. This is the safe default: a model's scratchpad
	// never reaches downstream tool loops.
	ReasoningStrip = "strip"
)

// thinkingBlock matches a single <think>...</think> block plus any
// trailing whitespace that separates it from the actual answer. The
// regex is non-greedy on the body and assumes a single block per
// assistant turn, which matches what the M3 vendor observed during
// the W7 acceptance run emits today. Nested or repeated blocks are
// still stripped; only the boundary case of a tag split across two
// stream chunks would survive (documented; flagged if it appears).
var thinkingBlock = regexp.MustCompile(`(?s)<think>.*?</think>\s*`)

// stripThinking returns content with interleaved-thinking blocks
// removed. Empty strings and strings without the markers are returned
// unchanged.
func stripThinking(content string) string {
	return thinkingBlock.ReplaceAllString(content, "")
}

// OpenAIConfig selects one explicitly enabled OpenAI-compatible vendor. The
// zero value is disabled: no client, no key, no network. Nothing is
// constructed until the first call, so disabled runners never dial.
type OpenAIConfig struct {
	Enabled    bool
	BaseURL    string
	APIKey     string
	Model      string
	HTTPClient *http.Client
	Timeout    time.Duration
	// Reasoning controls interleaved-thinking handling. The empty
	// string defaults to ReasoningStrip so a model's scratchpad never
	// leaks into downstream tool loops. Set to ReasoningOff to keep
	// content verbatim. Other values are rejected by the constructor.
	Reasoning string
	// ReasoningSplit asks compatible vendors to return the thinking
	// text in a separate `reasoning_details` field. When true, the
	// adapter sets `reasoning_split: true` in the request body and
	// still strips <think> blocks from `content` for safety. A typed
	// parser for `reasoning_details` is left as a follow-up — until
	// then, callers see stripped content regardless.
	ReasoningSplit bool
}

// stripThinking reports whether the adapter should strip <think>
// blocks from assistant content. Anything other than ReasoningOff
// (case-insensitive, trimmed) strips; the default zero value therefore
// strips, which is the safe fallback.
func (c OpenAIConfig) stripThinkingEnabled() bool {
	return !strings.EqualFold(strings.TrimSpace(c.Reasoning), ReasoningOff)
}

// OpenAIChatModel adapts any OpenAI-compatible /chat/completions endpoint to
// the eino chat surface. Vendor usage is reported exactly; silence falls back
// to the runner's conservative reservation.
type OpenAIChatModel struct {
	config OpenAIConfig
	client *http.Client
}

var _ einomodel.BaseChatModel = (*OpenAIChatModel)(nil)

// NewOpenAIChatModel validates explicit enablement. Disabled or incomplete
// configuration is an error here, not a silent fallback.
func NewOpenAIChatModel(config OpenAIConfig) (*OpenAIChatModel, error) {
	if !config.Enabled {
		return nil, fmt.Errorf("openai vendor is disabled")
	}
	if strings.TrimSpace(config.BaseURL) == "" || strings.TrimSpace(config.Model) == "" {
		return nil, fmt.Errorf("openai vendor needs base_url and model")
	}
	if strings.TrimSpace(config.APIKey) == "" {
		return nil, fmt.Errorf("openai vendor needs an api key")
	}
	if mode := strings.ToLower(strings.TrimSpace(config.Reasoning)); mode != "" && mode != ReasoningOff && mode != ReasoningStrip {
		return nil, fmt.Errorf("openai vendor reasoning %q is invalid (supported: %q, %q)", config.Reasoning, ReasoningOff, ReasoningStrip)
	}
	client := config.HTTPClient
	if client == nil {
		timeout := config.Timeout
		if timeout <= 0 {
			timeout = 60 * time.Second
		}
		client = &http.Client{Timeout: timeout}
	}
	return &OpenAIChatModel{config: config, client: client}, nil
}

type openAIMessage struct {
	Role       string         `json:"role"`
	Content    string         `json:"content,omitempty"`
	ToolCalls  []openAITool   `json:"tool_calls,omitempty"`
	ToolCallID string         `json:"tool_call_id,omitempty"`
	Name       string         `json:"name,omitempty"`
	Extra      map[string]any `json:"-"`
}

type openAITool struct {
	ID       string `json:"id"`
	Index    *int   `json:"index,omitempty"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type openAIRequest struct {
	Model          string          `json:"model"`
	Messages       []openAIMessage `json:"messages"`
	Tools          []openAIToolDef `json:"tools,omitempty"`
	Stream         bool            `json:"stream"`
	StreamOptions  *streamOptions  `json:"stream_options,omitempty"`
	MaxTokens      int             `json:"max_tokens,omitempty"`
	Temperature    *float64        `json:"temperature,omitempty"`
	// ReasoningSplit is the vendor-specific extension that asks for the
	// scratchpad to be returned in `reasoning_details`. Some OpenAI-
	// compatible vendors (MiniMax M3 included) honour it.
	ReasoningSplit bool `json:"reasoning_split,omitempty"`
}

type streamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type openAIToolDef struct {
	Type     string         `json:"type"`
	Function openAIFunction `json:"function"`
}

type openAIFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	Parameters  map[string]any `json:"parameters,omitempty"`
}

type openAIResponse struct {
	Choices []struct {
		Message struct {
			Role      string       `json:"role"`
			Content   string       `json:"content"`
			ToolCalls []openAITool `json:"tool_calls"`
		} `json:"message"`
		Delta struct {
			Role      string       `json:"role"`
			Content   string       `json:"content"`
			ToolCalls []openAITool `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// Generate performs one non-streaming completion.
func (m *OpenAIChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.Message, error) {
	options := einomodel.GetCommonOptions(&einomodel.Options{}, opts...)
	request := openAIRequest{
		Model:          m.config.Model,
		Messages:       toOpenAIMessages(input),
		Stream:         false,
		ReasoningSplit: m.config.ReasoningSplit,
	}
	if options.MaxTokens != nil && *options.MaxTokens > 0 {
		request.MaxTokens = *options.MaxTokens
	}
	for _, tool := range options.Tools {
		parameters, err := vendorParameters(tool)
		if err != nil {
			return nil, fmt.Errorf("tool %q has an unsupported parameter schema: %w", tool.Name, err)
		}
		request.Tools = append(request.Tools, openAIToolDef{
			Type: "function",
			Function: openAIFunction{
				Name:        tool.Name,
				Description: tool.Desc,
				Parameters:  parameters,
			},
		})
	}
	var response openAIResponse
	if err := m.post(ctx, request, &response); err != nil {
		return nil, err
	}
	if len(response.Choices) == 0 {
		return nil, fmt.Errorf("openai vendor returned no choices")
	}
	choice := response.Choices[0].Message
	answer := &schema.Message{Role: schema.Assistant, Content: choice.Content}
	if m.config.stripThinkingEnabled() {
		answer.Content = stripThinking(answer.Content)
	}
	for _, call := range choice.ToolCalls {
		answer.ToolCalls = append(answer.ToolCalls, schema.ToolCall{
			ID:       call.ID,
			Type:     "function",
			Function: schema.FunctionCall{Name: call.Function.Name, Arguments: call.Function.Arguments},
		})
	}
	if total := response.Usage.TotalTokens; total > 0 {
		answer.ResponseMeta = &schema.ResponseMeta{Usage: &schema.TokenUsage{
			PromptTokens:     response.Usage.PromptTokens,
			CompletionTokens: response.Usage.CompletionTokens,
			TotalTokens:      total,
		}}
	}
	return answer, nil
}

// Stream performs one SSE completion with true incremental delivery: the
// first vendor frame reaches Recv callers before the upstream finishes.
// Usage is requested explicitly and attached to the trailing chunk.
func (m *OpenAIChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...einomodel.Option) (*schema.StreamReader[*schema.Message], error) {
	options := einomodel.GetCommonOptions(&einomodel.Options{}, opts...)
	request := openAIRequest{
		Model:          m.config.Model,
		Messages:       toOpenAIMessages(input),
		Stream:         true,
		StreamOptions:  &streamOptions{IncludeUsage: true},
		ReasoningSplit: m.config.ReasoningSplit,
	}
	if options.MaxTokens != nil && *options.MaxTokens > 0 {
		request.MaxTokens = *options.MaxTokens
	}
	for _, tool := range options.Tools {
		parameters, err := vendorParameters(tool)
		if err != nil {
			return nil, fmt.Errorf("tool %q has an unsupported parameter schema: %w", tool.Name, err)
		}
		request.Tools = append(request.Tools, openAIToolDef{
			Type: "function",
			Function: openAIFunction{
				Name:        tool.Name,
				Description: tool.Desc,
				Parameters:  parameters,
			},
		})
	}
	chunks, err := m.postStream(ctx, request)
	if err != nil {
		return nil, err
	}
	return chunks, nil
}

func toOpenAIMessages(input []*schema.Message) []openAIMessage {
	messages := make([]openAIMessage, 0, len(input))
	for _, message := range input {
		if message == nil {
			continue
		}
		converted := openAIMessage{Content: message.Content, Name: message.Name}
		switch message.Role {
		case schema.User:
			converted.Role = "user"
		case schema.System:
			converted.Role = "system"
		case schema.Tool:
			converted.Role = "tool"
			converted.ToolCallID = message.ToolCallID
		default:
			converted.Role = "assistant"
		}
		for _, call := range message.ToolCalls {
			converted.ToolCalls = append(converted.ToolCalls, openAITool{
				ID:   call.ID,
				Type: "function",
				Function: struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				}{Name: call.Function.Name, Arguments: call.Function.Arguments},
			})
		}
		messages = append(messages, converted)
	}
	return messages
}

func (m *OpenAIChatModel) post(ctx context.Context, request openAIRequest, response *openAIResponse) error {
	body, err := json.Marshal(request)
	if err != nil {
		return err
	}
	endpoint := strings.TrimRight(m.config.BaseURL, "/") + "/chat/completions"
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Authorization", "Bearer "+m.config.APIKey)
	httpResponse, err := m.client.Do(httpRequest)
	if err != nil {
		return fmt.Errorf("openai vendor request failed: %w", err)
	}
	defer httpResponse.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(httpResponse.Body, 4<<20))
	if err != nil {
		return fmt.Errorf("openai vendor read failed: %w", err)
	}
	if httpResponse.StatusCode != http.StatusOK {
		return fmt.Errorf("openai vendor status %d: %s", httpResponse.StatusCode, cappedVendorBody(payload))
	}
	if err := json.Unmarshal(payload, response); err != nil {
		return fmt.Errorf("openai vendor decode failed: %w", err)
	}
	return nil
}

func (m *OpenAIChatModel) postStream(ctx context.Context, request openAIRequest) (*schema.StreamReader[*schema.Message], error) {
	body, err := json.Marshal(request)
	if err != nil {
		return nil, err
	}
	endpoint := strings.TrimRight(m.config.BaseURL, "/") + "/chat/completions"
	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpRequest.Header.Set("Content-Type", "application/json")
	httpRequest.Header.Set("Authorization", "Bearer "+m.config.APIKey)
	httpRequest.Header.Set("Accept", "text/event-stream")
	httpResponse, err := m.client.Do(httpRequest)
	if err != nil {
		return nil, fmt.Errorf("openai vendor stream failed: %w", err)
	}
	if httpResponse.StatusCode != http.StatusOK {
		payload, _ := io.ReadAll(io.LimitReader(httpResponse.Body, 64<<10))
		_ = httpResponse.Body.Close()
		return nil, fmt.Errorf("openai vendor stream status %d: %s", httpResponse.StatusCode, cappedVendorBody(payload))
	}
	reader, writer := schema.Pipe[*schema.Message](16)
	go pumpSSE(ctx, httpResponse.Body, writer, m.config.stripThinkingEnabled())
	return reader, nil
}

// pumpSSE parses vendor SSE incrementally and forwards frames as they
// arrive: text deltas immediately, tool calls once their fragments complete,
// usage on the trailing chunk. A disconnected consumer (closed == true)
// stops the pump; the body always closes. When strip is true, each
// content delta has <think>...</think> blocks removed before it reaches
// the consumer; strip is applied per chunk so a tag split across two
// frames survives as-is (documented; flagged if observed).
func pumpSSE(ctx context.Context, body io.Reader, writer *schema.StreamWriter[*schema.Message], strip bool) {
	defer writer.Close()
	closer, _ := body.(io.Closer)
	if closer != nil {
		defer closer.Close()
	}
	scanner := bufio.NewScanner(body)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	// Calls accumulate per vendor index until the round completes: vendor
	// frames may interleave indexes (0/1/0/1) and continuation frames
	// usually carry neither id nor name, so flushing on index change would
	// split one call into several empty-ID fragments. Text deltas still
	// stream immediately; complete calls emit when the round ends.
	merged := map[int]*schema.ToolCall{}
	flushCalls := func() bool {
		indexes := make([]int, 0, len(merged))
		for index := range merged {
			indexes = append(indexes, index)
		}
		sort.Ints(indexes)
		for _, index := range indexes {
			call := *merged[index]
			delete(merged, index)
			if writer.Send(&schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{call}}, nil) {
				return false
			}
		}
		return true
	}
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			writer.Send(nil, err)
			return
		}
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			flushCalls()
			break
		}
		var event openAIResponse
		if err := json.Unmarshal([]byte(payload), &event); err != nil {
			continue
		}
		if event.Usage.TotalTokens > 0 {
			usage := event.Usage
			if writer.Send(&schema.Message{Role: schema.Assistant, ResponseMeta: &schema.ResponseMeta{Usage: &schema.TokenUsage{
				PromptTokens:     usage.PromptTokens,
				CompletionTokens: usage.CompletionTokens,
				TotalTokens:      usage.TotalTokens,
			}}}, nil) {
				return
			}
			continue
		}
		if len(event.Choices) == 0 {
			continue
		}
		delta := event.Choices[0].Delta
		if delta.Content != "" {
			content := delta.Content
			if strip {
				content = stripThinking(delta.Content)
			}
			if content != "" {
				if writer.Send(&schema.Message{Role: schema.Assistant, Content: content}, nil) {
					return
				}
			}
		}
		for _, call := range delta.ToolCalls {
			index := 0
			if call.Index != nil {
				index = *call.Index
			}
			existing, ok := merged[index]
			if !ok {
				existing = &schema.ToolCall{ID: call.ID, Type: "function"}
				merged[index] = existing
			}
			if call.ID != "" {
				existing.ID = call.ID
			}
			existing.Function.Name += call.Function.Name
			existing.Function.Arguments += call.Function.Arguments
		}
	}
	flushCalls()
	if err := scanner.Err(); err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			writer.Send(nil, ctxErr)
		} else {
			writer.Send(nil, fmt.Errorf("openai vendor stream read failed: %w", err))
		}
	}
}

// cappedVendorBody keeps vendor error bodies small and credential-shaped
// content out of errors.
func cappedVendorBody(payload []byte) string {
	text := string(payload)
	if len(text) > 512 {
		text = text[:512] + "..."
	}
	lower := strings.ToLower(text)
	for _, key := range []string{"api_key", "apikey", "authorization", "bearer"} {
		if at := strings.Index(lower, key); at >= 0 {
			end := at + len(key) + 24
			if end > len(text) {
				end = len(text)
			}
			text = text[:at] + key + "=***" + text[end:]
			lower = strings.ToLower(text)
		}
	}
	return text
}
