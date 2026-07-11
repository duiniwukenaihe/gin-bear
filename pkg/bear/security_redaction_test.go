package bear

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

type observablePanicErrorKey struct {
	calls *atomic.Int32
}

func (key observablePanicErrorKey) Error() string {
	key.calls.Add(1)
	panic("error-key-password-secret")
}

type observableRecursiveStringerKey struct {
	calls *atomic.Int32
}

func (key observableRecursiveStringerKey) String() string {
	if key.calls.Add(1) > 1 {
		panic("stringer-key-password-secret")
	}
	return formatObservableRecursiveKey(key)
}

func formatObservableRecursiveKey(value any) string {
	return fmt.Sprint(value)
}

type observableLoopError struct {
	next  error
	calls *atomic.Int32
}

func (err *observableLoopError) Error() string {
	err.calls.Add(1)
	panic("loop-error-password-secret")
}

func (err *observableLoopError) Unwrap() error {
	return err.next
}

type observableJoinError struct {
	children []error
}

func (err observableJoinError) Error() string {
	return "joined-error-password-secret"
}

func (err observableJoinError) Unwrap() []error {
	return err.children
}

type observablePanicUnwrapError struct{}

func (observablePanicUnwrapError) Error() string {
	return "panic-unwrap-password-secret"
}

func (observablePanicUnwrapError) Unwrap() error {
	panic("panic-unwrap-password-secret")
}

func TestContextHandlerRedactsSensitiveObservableData(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(&ContextHandler{Handler: slog.NewJSONHandler(&output, nil)})
	logger.With("secret", "with-secret").Error(
		"request failed password=message-secret",
		"authorization", "Bearer abc.def.ghi",
		"cookie", "session=cookie-secret",
		"dsn", "postgres://user:dsn-secret@db/app?sslmode=disable",
		"query", "access_token=query-secret",
		"error", errors.New("password=error-secret"),
		"error_category", "token_expired",
		"safe", "retained",
	)

	logged := output.String()
	for _, forbidden := range []string{
		"with-secret", "message-secret", "abc.def.ghi", "cookie-secret",
		"dsn-secret", "query-secret", "error-secret",
	} {
		if strings.Contains(logged, forbidden) {
			t.Fatalf("log leaked %q: %s", forbidden, logged)
		}
	}
	if !strings.Contains(logged, "retained") || !strings.Contains(logged, "token_expired") || !strings.Contains(logged, "[REDACTED]") {
		t.Fatalf("log did not preserve safe data and mark redaction: %s", logged)
	}
}

func TestSanitizeForObservabilityReturnsStableErrorCategory(t *testing.T) {
	got := SanitizeForObservability(errors.New("postgres://user:password@db/app?token=secret"))
	if got != "internal_error" {
		t.Fatalf("SanitizeForObservability = %q, want internal_error", got)
	}
	if got := SanitizeForObservability("opaque panic detail"); got != "[REDACTED]" {
		t.Fatalf("SanitizeForObservability panic value = %q, want redacted", got)
	}
}

func TestContextHandlerSafelySanitizesHostileMapKeys(t *testing.T) {
	panicErrorKey := observablePanicErrorKey{calls: new(atomic.Int32)}
	recursiveStringerKey := observableRecursiveStringerKey{calls: new(atomic.Int32)}
	value := map[any]any{
		"password=map-key-secret": "map-value-secret",
		panicErrorKey:             "safe-panic-key-value",
		recursiveStringerKey:      "safe-recursive-key-value",
	}

	var output bytes.Buffer
	logger := slog.New(&ContextHandler{Handler: slog.NewJSONHandler(&output, nil)})
	var recovered any
	func() {
		defer func() { recovered = recover() }()
		logger.Info("hostile map keys", "payload", value)
	}()
	if recovered != nil {
		t.Fatalf("structured logging panicked for map key: %v", recovered)
	}
	if got := panicErrorKey.calls.Load(); got != 0 {
		t.Fatalf("error map key Error calls = %d, want 0", got)
	}
	if got := recursiveStringerKey.calls.Load(); got != 0 {
		t.Fatalf("Stringer map key calls = %d, want 0", got)
	}

	logged := output.String()
	for _, forbidden := range []string{
		"map-key-secret", "map-value-secret", "error-key-password-secret",
		"stringer-key-password-secret", "loop-error-password-secret",
	} {
		if strings.Contains(logged, forbidden) {
			t.Fatalf("map-key sanitizer leaked %q: %s", forbidden, logged)
		}
	}
	for _, required := range []string{"safe-panic-key-value", "safe-recursive-key-value", "[REDACTED]"} {
		if !strings.Contains(logged, required) {
			t.Fatalf("map-key sanitizer lost %q: %s", required, logged)
		}
	}
}

func TestSanitizeForObservabilityBoundsUnsafeErrorChains(t *testing.T) {
	self := &observableLoopError{calls: new(atomic.Int32)}
	self.next = self
	first := &observableLoopError{calls: new(atomic.Int32)}
	second := &observableLoopError{calls: new(atomic.Int32)}
	first.next = second
	second.next = first

	for _, test := range []struct {
		name string
		err  error
		want string
	}{
		{name: "self cycle", err: self, want: "internal_error"},
		{name: "mutual cycle", err: first, want: "internal_error"},
		{
			name: "unwrap slice",
			err:  observableJoinError{children: []error{errors.New("generic"), context.DeadlineExceeded}},
			want: "deadline_exceeded",
		},
		{name: "panicking unwrap", err: observablePanicUnwrapError{}, want: "internal_error"},
	} {
		t.Run(test.name, func(t *testing.T) {
			result := make(chan string, 1)
			go func() { result <- SanitizeForObservability(test.err) }()
			select {
			case got := <-result:
				if got != test.want {
					t.Fatalf("SanitizeForObservability() = %q, want %q", got, test.want)
				}
			case <-time.After(100 * time.Millisecond):
				t.Fatalf("SanitizeForObservability did not return for %s", test.name)
			}
		})
	}
	if got := self.calls.Load(); got != 0 {
		t.Fatalf("self-cycle Error calls = %d, want 0", got)
	}
	if got := first.calls.Load() + second.calls.Load(); got != 0 {
		t.Fatalf("mutual-cycle Error calls = %d, want 0", got)
	}
}

func TestContextHandlerLocallyRedactsSecretsAndPreservesDiagnostics(t *testing.T) {
	var output bytes.Buffer
	logger := slog.New(&ContextHandler{Handler: slog.NewJSONHandler(&output, nil)})
	logger.Error(
		"token validation failed; Authorization: Bearer auth-secret; password=message-secret; endpoint=https://alice:url-secret@api.example/v1/orders?token=query-secret; retrying",
		"postgres_connection", "host=db.example user=alice password='postgres secret' dbname=app sslmode=verify-full",
		"mysql_connection", "alice:mysql-secret@tcp(db.example:3306)/app?token=query-secret",
		"error_category", "token_invalid",
	)

	logged := output.String()
	for _, forbidden := range []string{
		"auth-secret", "message-secret", "alice:url-secret", "query-secret",
		"postgres secret", "mysql-secret",
	} {
		if strings.Contains(logged, forbidden) {
			t.Fatalf("log leaked %q: %s", forbidden, logged)
		}
	}
	for _, required := range []string{
		"token validation failed", "retrying", "Authorization: Bearer [REDACTED]",
		"password=[REDACTED]", "https://api.example/v1/orders", "host=db.example",
		"dbname=app", "tcp(db.example:3306)/app", "token_invalid",
	} {
		if !strings.Contains(logged, required) {
			t.Fatalf("log lost safe diagnostic %q: %s", required, logged)
		}
	}
}

func TestContextHandlerRecursivelyRedactsCommonStructuredValues(t *testing.T) {
	type payload struct {
		Host     string
		Path     string
		Password string
		Endpoint *url.URL
		Headers  http.Header
		Nested   map[string]any
		Items    []any
		Self     *payload
	}

	endpoint := &url.URL{
		Scheme:   "https",
		User:     url.UserPassword("alice", "userinfo-secret"),
		Host:     "api.example",
		Path:     "/v1/orders",
		RawQuery: "token=url-query-secret",
	}
	value := &payload{
		Host:     "worker.example",
		Path:     "/jobs/:id",
		Password: "struct-password-secret",
		Endpoint: endpoint,
		Headers: http.Header{
			"Authorization": {"Bearer header-secret"},
			"Cookie":        {"sid=cookie-secret"},
			"X-Trace-ID":    {"trace-123"},
		},
		Nested: map[string]any{
			"query": map[string]string{"token": "nested-query-secret"},
			"error": errors.New("password=nested-error-secret"),
			"safe":  "token validation failed",
		},
		Items: []any{
			map[string]any{"secret": "slice-secret", "host": "slice.example"},
			errors.New("authorization=slice-error-secret"),
			"ordinary diagnostic",
		},
	}
	value.Self = value

	var output bytes.Buffer
	logger := slog.New(&ContextHandler{Handler: slog.NewJSONHandler(&output, nil)})
	logger.Info("structured event",
		"payload", value,
		"request", slog.GroupValue(
			slog.String("token", "group-secret"),
			slog.String("host", "group.example"),
		),
	)

	logged := output.String()
	for _, forbidden := range []string{
		"userinfo-secret", "url-query-secret", "struct-password-secret",
		"header-secret", "cookie-secret", "nested-query-secret", "nested-error-secret",
		"slice-secret", "slice-error-secret", "group-secret",
	} {
		if strings.Contains(logged, forbidden) {
			t.Fatalf("structured log leaked %q: %s", forbidden, logged)
		}
	}
	for _, required := range []string{
		"worker.example", "/jobs/:id", "https://api.example/v1/orders", "trace-123",
		"token validation failed", "internal_error", "slice.example", "ordinary diagnostic",
		"group.example", "[CYCLE]", "[REDACTED]",
	} {
		if !strings.Contains(logged, required) {
			t.Fatalf("structured log lost safe value %q: %s", required, logged)
		}
	}
}

func TestContextHandlerBoundsNestedDepthAndCollectionLength(t *testing.T) {
	deep := map[string]any{"leaf": "safe-leaf"}
	for i := 0; i < 20; i++ {
		deep = map[string]any{fmt.Sprintf("level_%02d", i): deep}
	}
	large := make([]any, 40)
	for i := range large {
		large[i] = fmt.Sprintf("item-%02d", i)
	}
	large[len(large)-1] = map[string]any{"password": "collection-secret"}

	var output bytes.Buffer
	logger := slog.New(&ContextHandler{Handler: slog.NewJSONHandler(&output, nil)})
	logger.Info("bounded", "deep", deep, "large", large)

	logged := output.String()
	if strings.Contains(logged, "collection-secret") || strings.Contains(logged, "safe-leaf") {
		t.Fatalf("bounded redactor traversed beyond its limits: %s", logged)
	}
	for _, required := range []string{"item-00", "[TRUNCATED]"} {
		if !strings.Contains(logged, required) {
			t.Fatalf("bounded redactor missing %q: %s", required, logged)
		}
	}
}

func TestWriteErrorLogsStableMetadataWithoutRawErrorOrPath(t *testing.T) {
	resetGinModeForTest(t)
	var output bytes.Buffer
	runtime := &Runtime{Logger: slog.New(slog.NewJSONHandler(&output, nil))}
	router := gin.New()
	router.Use(func(ctx *gin.Context) {
		ctx.Set(runtimeContextKey, runtime)
		ctx.Next()
	})
	router.GET("/users/:id", func(ctx *gin.Context) {
		WriteError(ctx, NewStatusError(
			http.StatusInternalServerError,
			91001,
			"error_internal",
			errors.New("password=http-error-secret"),
		))
	})

	response := performRequest(router, httptest.NewRequest(
		http.MethodGet,
		"/users/private-user-id?token=query-secret",
		nil,
	))
	if response.Code != http.StatusInternalServerError || strings.Contains(response.Body.String(), "http-error-secret") {
		t.Fatalf("response = %d %s", response.Code, response.Body.String())
	}

	logged := output.String()
	for _, forbidden := range []string{"http-error-secret", "private-user-id", "query-secret"} {
		if strings.Contains(logged, forbidden) {
			t.Fatalf("HTTP error log leaked %q: %s", forbidden, logged)
		}
	}
	for _, required := range []string{`"route":"/users/:id"`, `"status":500`, `"error_code":91001`, `"error_category":"bear_error"`} {
		if !strings.Contains(logged, required) {
			t.Fatalf("HTTP error log missing %s: %s", required, logged)
		}
	}
}

func TestTracingRecordsSafeErrorMetadataWithoutGinErrorMessage(t *testing.T) {
	resetGinModeForTest(t)
	exporter := tracetest.NewInMemoryExporter()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSyncer(exporter))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })

	router := gin.New()
	router.Use(TracingMiddleware(provider, propagation.TraceContext{}))
	router.GET("/orders/:id", func(ctx *gin.Context) {
		_ = ctx.Error(NewStatusError(
			http.StatusBadRequest,
			42001,
			"error_invalid_params",
			errors.New("authorization=trace-secret"),
		))
		ctx.Status(http.StatusBadRequest)
	})

	response := performRequest(router, httptest.NewRequest(http.MethodGet, "/orders/42", nil))
	if response.Code != http.StatusBadRequest {
		t.Fatalf("status = %d", response.Code)
	}
	spans := exporter.GetSpans()
	if len(spans) != 1 || len(spans[0].Events) != 1 {
		t.Fatalf("spans/events = %#v", spans)
	}
	event := spans[0].Events[0]
	if event.Name != "gin.error" {
		t.Fatalf("event name = %q", event.Name)
	}
	for _, attr := range event.Attributes {
		if strings.Contains(attr.Value.Emit(), "trace-secret") || string(attr.Key) == "error.message" {
			t.Fatalf("trace event leaked raw error in %#v", attr)
		}
	}
	if !spanHasStringAttr(event.Attributes, "error.type", "bear_error") {
		t.Fatalf("event missing safe error type: %#v", event.Attributes)
	}
	if !spanHasIntAttr(event.Attributes, "gin_bear.error_code", 42001) {
		t.Fatalf("event missing stable error code: %#v", event.Attributes)
	}
}
