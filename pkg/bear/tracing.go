package bear

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/exporters/stdout/stdouttrace"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	oteltrace "go.opentelemetry.io/otel/trace"
)

const tracerName = "github.com/duiniwukenaihe/gin-bear/pkg/bear"

// TracingMiddleware creates OpenTelemetry server spans for Gin HTTP requests.
func TracingMiddleware(provider oteltrace.TracerProvider, propagator propagation.TextMapPropagator) gin.HandlerFunc {
	if provider == nil {
		provider = otel.GetTracerProvider()
	}
	if propagator == nil {
		propagator = otel.GetTextMapPropagator()
	}
	tracer := provider.Tracer(tracerName)
	return func(c *gin.Context) {
		extracted := propagator.Extract(c.Request.Context(), propagation.HeaderCarrier(c.Request.Header))
		route := tracingRoute(c)
		spanName := c.Request.Method + " " + route
		ctx, span := tracer.Start(extracted, spanName,
			oteltrace.WithSpanKind(oteltrace.SpanKindServer),
			oteltrace.WithAttributes(
				attribute.String("http.request.method", c.Request.Method),
				attribute.String("url.path", c.Request.URL.Path),
				attribute.String("url.query", c.Request.URL.RawQuery),
				attribute.String("http.route", route),
				attribute.String("client.address", c.ClientIP()),
			),
		)
		c.Request = c.Request.WithContext(ctx)
		defer span.End()

		c.Next()

		finalRoute := tracingRoute(c)
		if finalRoute != route {
			span.SetName(c.Request.Method + " " + finalRoute)
			span.SetAttributes(attribute.String("http.route", finalRoute))
		}
		status := c.Writer.Status()
		span.SetAttributes(attribute.Int("http.response.status_code", status))
		if requestID := c.GetHeader("X-Request-ID"); requestID != "" {
			span.SetAttributes(attribute.String("gin_bear.request_id", requestID))
		}
		if status >= http.StatusInternalServerError {
			span.SetStatus(codes.Error, http.StatusText(status))
		}
		for _, ginErr := range c.Errors {
			span.AddEvent("gin.error", oteltrace.WithAttributes(attribute.String("error.message", ginErr.Error())))
		}
	}
}

func tracingRoute(c *gin.Context) string {
	if route := c.FullPath(); route != "" {
		return route
	}
	if c.Request != nil && c.Request.URL != nil && c.Request.URL.Path != "" {
		return c.Request.URL.Path
	}
	return "/"
}

func newTracerProvider(ctx context.Context, cfg *TracingConfig) (*sdktrace.TracerProvider, error) {
	if cfg == nil {
		cfg = &TracingConfig{}
	}
	serviceName := cfg.ServiceName
	if strings.TrimSpace(serviceName) == "" {
		serviceName = "gin-bear"
	}
	sampleRate := cfg.SampleRate
	if sampleRate <= 0 {
		sampleRate = 0
	}
	if sampleRate > 1 {
		sampleRate = 1
	}
	options := []sdktrace.TracerProviderOption{
		sdktrace.WithSampler(sdktrace.ParentBased(sdktrace.TraceIDRatioBased(sampleRate))),
		sdktrace.WithResource(resource.NewWithAttributes("", attribute.String("service.name", serviceName))),
	}

	switch strings.ToLower(strings.TrimSpace(cfg.Exporter)) {
	case "", "none", "noop":
	case "stdout", "console":
		exporter, err := stdouttrace.New(stdouttrace.WithPrettyPrint())
		if err != nil {
			return nil, fmt.Errorf("create stdout trace exporter: %w", err)
		}
		options = append(options, sdktrace.WithBatcher(exporter))
	case "otlp", "otlphttp":
		exporterOptions := []otlptracehttp.Option{}
		if strings.TrimSpace(cfg.OTLPEndpoint) != "" {
			exporterOptions = append(exporterOptions, otlptracehttp.WithEndpointURL(cfg.OTLPEndpoint))
		}
		exporter, err := otlptracehttp.New(ctx, exporterOptions...)
		if err != nil {
			return nil, fmt.Errorf("create otlp trace exporter: %w", err)
		}
		options = append(options, sdktrace.WithBatcher(exporter))
	default:
		return nil, fmt.Errorf("unsupported tracing exporter %q", cfg.Exporter)
	}

	return sdktrace.NewTracerProvider(options...), nil
}

func shutdownTracerProvider(provider *sdktrace.TracerProvider) func() {
	return func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = provider.Shutdown(ctx)
	}
}
