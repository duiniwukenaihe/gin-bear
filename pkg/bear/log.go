package bear

import (
	"context"
	"log/slog"
	"os"
)

var Log *slog.Logger

// ContextHandler 自动从 context 中提取 request_id 并注入日志
type ContextHandler struct {
	slog.Handler
}

// Handle 实现 slog.Handler 接口
func (h *ContextHandler) Handle(ctx context.Context, r slog.Record) error {
	if ctx == nil {
		return h.Handler.Handle(ctx, r)
	}
	if rid, ok := ctx.Value(RequestIDKey).(string); ok {
		r.AddAttrs(slog.String(string(RequestIDKey), rid))
	}
	return h.Handler.Handle(ctx, r)
}

// SetDefaultLogger 初始化全局上下文感知日志
func SetDefaultLogger() {
	opts := &slog.HandlerOptions{
		Level: slog.LevelInfo,
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				a.Value = slog.StringValue(a.Value.Time().Format("2006-01-02T15:04:05.000Z07:00"))
			}
			return a
		},
	}
	handler := &ContextHandler{
		Handler: slog.NewJSONHandler(os.Stdout, opts),
	}
	Log = slog.New(handler)
	slog.SetDefault(Log)
}

func Info(msg string, args ...any) {
	slog.Info(msg, args...)
}

func ErrorLog(msg string, args ...any) {
	slog.Error(msg, args...)
}

func Warn(msg string, args ...any) {
	slog.Warn(msg, args...)
}

func Debug(msg string, args ...any) {
	slog.Debug(msg, args...)
}

func InfoContext(ctx context.Context, msg string, args ...any) {
	slog.InfoContext(ctx, msg, args...)
}

func ErrorContext(ctx context.Context, msg string, args ...any) {
	slog.ErrorContext(ctx, msg, args...)
}

func WarnContext(ctx context.Context, msg string, args ...any) {
	slog.WarnContext(ctx, msg, args...)
}

func DebugContext(ctx context.Context, msg string, args ...any) {
	slog.DebugContext(ctx, msg, args...)
}

func WithContext(ctx context.Context) *slog.Logger {
	if ctx == nil {
		return slog.Default()
	}
	if rid, ok := ctx.Value(RequestIDKey).(string); ok {
		return slog.Default().With(string(RequestIDKey), rid)
	}
	return slog.Default()
}
