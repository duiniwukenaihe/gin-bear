package agent

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	einomodel "github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
)

// TestOpenAIAdapterRoundTrip proves the vendor adapter against the wire
// protocol: tool binding, usage reports, and error mapping. A live vendor
// call still requires operator credentials and is NOT_RUN here.
func TestOpenAIAdapterRoundTrip(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/chat/completions" {
			http.NotFound(writer, request)
			return
		}
		var decoded struct {
			Model    string `json:"model"`
			Stream   bool   `json:"stream"`
			Messages []struct {
				Role string `json:"role"`
			} `json:"messages"`
			Tools []struct {
				Function struct {
					Name string `json:"name"`
				} `json:"function"`
			} `json:"tools"`
		}
		body, _ := io.ReadAll(request.Body)
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Errorf("bad vendor request: %v", err)
		}
		if decoded.Model != "stub-model" || len(decoded.Tools) != 1 || decoded.Tools[0].Function.Name != "get_order" {
			t.Errorf("vendor request = %+v", decoded)
		}
		if decoded.Stream {
			writer.Header().Set("Content-Type", "text/event-stream")
			_, _ = writer.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"order \"}}]}\n\ndata: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"c1\",\"function\":{\"name\":\"get_order\",\"arguments\":\"{\\\"order_id\\\":\\\"9\\\"}\"}}]}}]}\n\ndata: [DONE]\n"))
			return
		}
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"order 9","tool_calls":[{"id":"c1","type":"function","function":{"name":"get_order","arguments":"{\"order_id\":\"9\"}"}}]}}],"usage":{"prompt_tokens":10,"completion_tokens":5,"total_tokens":15}}`))
	}))
	defer server.Close()

	model, err := NewOpenAIChatModel(OpenAIConfig{Enabled: true, BaseURL: server.URL, APIKey: "stub-key", Model: "stub-model"})
	if err != nil {
		t.Fatal(err)
	}
	answer, err := model.Generate(context.Background(),
		[]*schema.Message{{Role: schema.User, Content: "hi"}},
		einomodel.WithTools([]*schema.ToolInfo{{Name: "get_order"}}),
	)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if answer.Content != "order 9" || len(answer.ToolCalls) != 1 || answer.ToolCalls[0].Function.Name != "get_order" {
		t.Fatalf("answer = %+v", answer)
	}
	if usage, ok := modelUsage(answer); !ok || usage != 15 {
		t.Fatalf("usage = %d, %v; want 15, true", usage, ok)
	}

	stream, err := model.Stream(context.Background(),
		[]*schema.Message{{Role: schema.User, Content: "hi"}},
		einomodel.WithTools([]*schema.ToolInfo{{Name: "get_order"}}),
	)
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	merged, err := collectStream(stream, nil)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if merged.Content != "order " || len(merged.ToolCalls) != 1 || merged.ToolCalls[0].Function.Arguments != `{"order_id":"9"}` {
		t.Fatalf("merged = %+v", merged)
	}
}

func TestOpenAIAdapterMapsVendorErrors(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.WriteHeader(http.StatusUnauthorized)
		_, _ = writer.Write([]byte(`{"error":{"message":"bad key"}}`))
	}))
	defer server.Close()
	model, err := NewOpenAIChatModel(OpenAIConfig{Enabled: true, BaseURL: server.URL, APIKey: "bad", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := model.Generate(context.Background(), []*schema.Message{{Role: schema.User, Content: "hi"}}); err == nil {
		t.Fatal("vendor 401 swallowed")
	} else if !strings.Contains(err.Error(), "401") {
		t.Fatalf("vendor error = %v, want status", err)
	}
}

func TestOpenAIAdapterHonorsCancel(t *testing.T) {
	model, err := NewOpenAIChatModel(OpenAIConfig{Enabled: true, BaseURL: "http://127.0.0.1:1", APIKey: "k", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := model.Generate(cancelled, []*schema.Message{{Role: schema.User, Content: "hi"}}); err == nil {
		t.Fatal("cancelled vendor call succeeded")
	}
}

// TestOpenAIAdapterSendsLimitsAndUsage is the R4 wire acceptance: output caps
// travel as max_tokens, streams request usage, and usage events reconcile.
func TestOpenAIAdapterSendsLimitsAndUsage(t *testing.T) {
	var sawMaxTokens int
	var sawIncludeUsage bool
	var sawParameters map[string]any
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var decoded struct {
			MaxTokens     int  `json:"max_tokens"`
			Stream        bool `json:"stream"`
			StreamOptions *struct {
				IncludeUsage bool `json:"include_usage"`
			} `json:"stream_options"`
			Tools []struct {
				Function struct {
					Name       string         `json:"name"`
					Parameters map[string]any `json:"parameters"`
				} `json:"function"`
			} `json:"tools"`
		}
		body, _ := io.ReadAll(request.Body)
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Errorf("bad vendor request: %v", err)
		}
		sawMaxTokens = decoded.MaxTokens
		if len(decoded.Tools) > 0 {
			sawParameters = decoded.Tools[0].Function.Parameters
		}
		if decoded.Stream {
			sawIncludeUsage = decoded.StreamOptions != nil && decoded.StreamOptions.IncludeUsage
			writer.Header().Set("Content-Type", "text/event-stream")
			_, _ = writer.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: {\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\ndata: [DONE]\n"))
			return
		}
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":3,"completion_tokens":2,"total_tokens":5}}`))
	}))
	defer server.Close()

	model, err := NewOpenAIChatModel(OpenAIConfig{Enabled: true, BaseURL: server.URL, APIKey: "k", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	declared := &schema.ToolInfo{
		Name: "get_order",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"order_id": {Type: schema.String, Required: true},
		}),
	}
	if _, err := model.Generate(context.Background(), []*schema.Message{{Role: schema.User, Content: "hi"}},
		einomodel.WithMaxTokens(77), einomodel.WithTools([]*schema.ToolInfo{declared})); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if sawMaxTokens != 77 {
		t.Fatalf("max_tokens = %d, want 77", sawMaxTokens)
	}
	properties, ok := sawParameters["properties"].(map[string]any)
	if !ok || properties["order_id"] == nil {
		t.Fatalf("parameters = %v, want order_id property", sawParameters)
	}
	if required, ok := sawParameters["required"].([]any); !ok || len(required) != 1 || required[0] != "order_id" {
		t.Fatalf("required = %v", sawParameters["required"])
	}
	if denied, ok := sawParameters["additionalProperties"].(bool); !ok || denied {
		t.Fatalf("additionalProperties = %v, want false", sawParameters["additionalProperties"])
	}
	stream, err := model.Stream(context.Background(), []*schema.Message{{Role: schema.User, Content: "hi"}},
		einomodel.WithMaxTokens(55))
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	merged, err := collectStream(stream, nil)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if sawMaxTokens != 55 || !sawIncludeUsage {
		t.Fatalf("stream limits: max_tokens=%d include_usage=%v", sawMaxTokens, sawIncludeUsage)
	}
	if usage, ok := modelUsage(merged); !ok || usage != 5 {
		t.Fatalf("stream usage = %d, %v; want 5, true", usage, ok)
	}
}

// TestOpenAIStreamMergesInterleavedToolCalls is the V2 acceptance: frames
// that interleave indexes, pack two calls in one frame, or continue without
// id/name must still produce complete calls — never empty-ID fragments.
func TestOpenAIStreamMergesInterleavedToolCalls(t *testing.T) {
	frames := []string{
		// Interleaved: a/b open, then a/b completions carry no id/name.
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"id":"a","function":{"name":"get_order","arguments":"{"}},{"index":1,"id":"b","function":{"name":"get_order","arguments":"{"}}]}}]}`,
		`{"choices":[{"delta":{"tool_calls":[{"index":0,"function":{"arguments":"\"x\":1}"}},{"index":1,"function":{"arguments":"\"x\":2}"}}]}}]}`,
		// Two calls packed in a single frame.
		`{"choices":[{"delta":{"tool_calls":[{"index":2,"id":"c","function":{"name":"get_order","arguments":"{\"x\":3}"}},{"index":3,"id":"d","function":{"name":"get_order","arguments":"{\"x\":4}"}}]}}]}`,
	}
	var body strings.Builder
	for _, frame := range frames {
		body.WriteString("data: " + frame + "\n\n")
	}
	body.WriteString("data: [DONE]\n")
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte(body.String()))
	}))
	defer server.Close()
	model, err := NewOpenAIChatModel(OpenAIConfig{Enabled: true, BaseURL: server.URL, APIKey: "k", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := model.Stream(context.Background(), []*schema.Message{{Role: schema.User, Content: "hi"}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	merged, err := collectStream(stream, nil)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(merged.ToolCalls) != 4 {
		t.Fatalf("tool calls = %+v, want 4 complete calls", merged.ToolCalls)
	}
	want := map[string]string{"a": `{"x":1}`, "b": `{"x":2}`, "c": `{"x":3}`, "d": `{"x":4}`}
	for _, call := range merged.ToolCalls {
		arguments, ok := want[call.ID]
		if !ok {
			t.Fatalf("unexpected call ID %q in %+v", call.ID, merged.ToolCalls)
		}
		if call.Function.Name != "get_order" || call.Function.Arguments != arguments {
			t.Fatalf("call %q = %+v, want complete arguments %s", call.ID, call, arguments)
		}
	}
}

// TestOpenAIStreamEmitsCompleteCallsOnEarlyTermination covers a stream that
// ends without [DONE]: accumulated calls still arrive whole.
func TestOpenAIStreamEmitsCompleteCallsOnEarlyTermination(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"id\":\"a\",\"function\":{\"name\":\"get_order\",\"arguments\":\"{\\\"x\\\":1}\"}}]}}]}\n\n"))
	}))
	defer server.Close()
	model, err := NewOpenAIChatModel(OpenAIConfig{Enabled: true, BaseURL: server.URL, APIKey: "k", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := model.Stream(context.Background(), []*schema.Message{{Role: schema.User, Content: "hi"}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	merged, err := collectStream(stream, nil)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if len(merged.ToolCalls) != 1 || merged.ToolCalls[0].ID != "a" || merged.ToolCalls[0].Function.Name != "get_order" {
		t.Fatalf("tool calls = %+v, want the single complete call", merged.ToolCalls)
	}
}

// vendor frame reaches Recv while the upstream is still open, instead of
// waiting for the complete response.
func TestOpenAIStreamDeliversFirstFrameEarly(t *testing.T) {
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		flusher, ok := writer.(http.Flusher)
		if !ok {
			http.Error(writer, "no flush", http.StatusInternalServerError)
			return
		}
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"first\"}}]}\n\n"))
		flusher.Flush()
		<-release
		_, _ = writer.Write([]byte("data: {\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\ndata: [DONE]\n"))
	}))
	defer server.Close()
	defer close(release)

	model, err := NewOpenAIChatModel(OpenAIConfig{Enabled: true, BaseURL: server.URL, APIKey: "k", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := model.Stream(context.Background(), []*schema.Message{{Role: schema.User, Content: "hi"}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	defer stream.Close()
	type firstResult struct {
		chunk *schema.Message
		err   error
	}
	first := make(chan firstResult, 1)
	go func() {
		chunk, err := stream.Recv()
		first <- firstResult{chunk: chunk, err: err}
	}()
	select {
	case got := <-first:
		if got.err != nil {
			t.Fatalf("first Recv: %v", got.err)
		}
		if got.chunk == nil || got.chunk.Content != "first" {
			t.Fatalf("first chunk = %+v", got.chunk)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("first frame did not arrive while upstream was still open")
	}
}

// TestStripThinkingDefaultRemovesModelScratchpad pins the safe default:
// when OpenAIConfig.Reasoning is the zero value, interleaved-thinking
// blocks never reach downstream tool loops. The regression we are
// protecting against is one where a vendor (MiniMax M3 in particular)
// emits <think>...</think> blocks in the assistant `content` and a
// tool loop treats that text as user data.
func TestStripThinkingDefaultRemovesModelScratchpad(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"<think>internal plan</think>The answer is 42."}}],"usage":{"prompt_tokens":1,"completion_tokens":2,"total_tokens":3}}`))
	}))
	defer server.Close()
	model, err := NewOpenAIChatModel(OpenAIConfig{Enabled: true, BaseURL: server.URL, APIKey: "k", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	answer, err := model.Generate(context.Background(), []*schema.Message{{Role: schema.User, Content: "ping"}})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if answer.Content != "The answer is 42." {
		t.Fatalf("content = %q, want thinking stripped", answer.Content)
	}
	if strings.Contains(answer.Content, "<think>") {
		t.Fatalf("content still contains <think>: %q", answer.Content)
	}
}

// TestStripThinkingOffPreservesRawContent is the explicit-opt-out path:
// Reasoning: "off" leaves content verbatim. Tool authors who want the raw
// scratchpad (debugging, eval work) take this branch knowingly.
func TestStripThinkingOffPreservesRawContent(t *testing.T) {
	raw := "<think>internal plan</think>The answer is 42."
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"` + raw + `"}}]}`))
	}))
	defer server.Close()
	model, err := NewOpenAIChatModel(OpenAIConfig{Enabled: true, BaseURL: server.URL, APIKey: "k", Model: "m", Reasoning: "off"})
	if err != nil {
		t.Fatal(err)
	}
	answer, err := model.Generate(context.Background(), []*schema.Message{{Role: schema.User, Content: "ping"}})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if answer.Content != raw {
		t.Fatalf("content = %q, want raw passthrough", answer.Content)
	}
}

// TestStripThinkingLeavesToolCallsIntact confirms that thinking-text
// stripping never touches tool_calls: only the `content` string is
// post-processed.
func TestStripThinkingLeavesToolCallsIntact(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"<think>calling tool</think>","tool_calls":[{"id":"call-1","type":"function","function":{"name":"get_order","arguments":"{\"order_id\":7}"}}]}}]}`))
	}))
	defer server.Close()
	model, err := NewOpenAIChatModel(OpenAIConfig{Enabled: true, BaseURL: server.URL, APIKey: "k", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	answer, err := model.Generate(context.Background(), []*schema.Message{{Role: schema.User, Content: "ping"}})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if answer.Content != "" {
		t.Fatalf("content = %q, want empty after stripping", answer.Content)
	}
	if len(answer.ToolCalls) != 1 || answer.ToolCalls[0].Function.Name != "get_order" {
		t.Fatalf("tool calls = %+v, want get_order", answer.ToolCalls)
	}
}

// TestStripThinkingStreamStripsPerChunk covers per-frame stripping of
// <think> blocks during streaming. The stub emits the thinking block in
// its own delta, then the answer split across two deltas; the merger
// must not surface any thinking text.
func TestStripThinkingStreamStripsPerChunk(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		writer.Header().Set("Content-Type", "text/event-stream")
		_, _ = writer.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"<think>internal plan</think>\"}}]}\n\n"))
		_, _ = writer.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"The \"}}]}\n\n"))
		_, _ = writer.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\"answer\"}}]}\n\n"))
		_, _ = writer.Write([]byte("data: {\"choices\":[{\"delta\":{\"content\":\".\"}}]}\n\n"))
		_, _ = writer.Write([]byte("data: [DONE]\n\n"))
	}))
	defer server.Close()
	model, err := NewOpenAIChatModel(OpenAIConfig{Enabled: true, BaseURL: server.URL, APIKey: "k", Model: "m"})
	if err != nil {
		t.Fatal(err)
	}
	stream, err := model.Stream(context.Background(), []*schema.Message{{Role: schema.User, Content: "ping"}})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}
	merged, err := collectStream(stream, nil)
	if err != nil {
		t.Fatalf("collect: %v", err)
	}
	if merged.Content != "The answer." {
		t.Fatalf("merged content = %q, want %q", merged.Content, "The answer.")
	}
	if strings.Contains(merged.Content, "<think>") {
		t.Fatalf("merged content still contains thinking tag: %q", merged.Content)
	}
}

// TestReasoningSplitSendsVendorFlag confirms the request body carries
// reasoning_split: true when the operator asks for the vendor-side
// reasoning_details payload. The wire match is what the M3 vendor
// accepts; the response parser for reasoning_details is left for the
// follow-up.
func TestReasoningSplitSendsVendorFlag(t *testing.T) {
	var sawSplit bool
	var sawSplitPresent bool
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		var decoded struct {
			ReasoningSplit bool `json:"reasoning_split"`
		}
		body, _ := io.ReadAll(request.Body)
		if err := json.Unmarshal(body, &decoded); err == nil {
			sawSplit = decoded.ReasoningSplit
			sawSplitPresent = strings.Contains(string(body), `"reasoning_split":true`)
		}
		_, _ = writer.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"ok"}}]}`))
	}))
	defer server.Close()
	model, err := NewOpenAIChatModel(OpenAIConfig{Enabled: true, BaseURL: server.URL, APIKey: "k", Model: "m", ReasoningSplit: true})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := model.Generate(context.Background(), []*schema.Message{{Role: schema.User, Content: "ping"}}); err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if !sawSplit {
		t.Fatalf("reasoning_split was not parsed to true; present=%v", sawSplitPresent)
	}
}

// TestNewOpenAIChatModelRejectsInvalidReasoningMode guards the
// constructor: misspelled modes must fail here instead of silently
// behaving like the default.
func TestNewOpenAIChatModelRejectsInvalidReasoningMode(t *testing.T) {
	_, err := NewOpenAIChatModel(OpenAIConfig{Enabled: true, BaseURL: "http://x", APIKey: "k", Model: "m", Reasoning: "scrub"})
	if err == nil || !strings.Contains(err.Error(), "reasoning") {
		t.Fatalf("expected reasoning rejection, got %v", err)
	}
}

// TestStripThinkingHelperUnit covers the regex helper directly so a
// future change to the pattern lands with a clear signal.
func TestStripThinkingHelperUnit(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"no tag", "answer only", "answer only"},
		{"single tag no newline", "<think>x</think>visible", "visible"},
		{"single tag with newline", "<think>x</think>\n\nvisible", "visible"},
		{"two tags", "<think>a</think>mid<think>b</think>end", "midend"},
		{"empty body", "", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := stripThinking(c.in); got != c.want {
				t.Fatalf("got %q, want %q", got, c.want)
			}
		})
	}
}
