package bear

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel/propagation"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

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
