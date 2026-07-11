package bear

import (
	"context"
	"log/slog"
	"os"
	"strings"
)

var bootstrapLogger = newLogger(nil)

// Log remains the process-wide compatibility logger while delegating each call
// to the logger in the current atomic legacy facade.
var Log = slog.New(legacyLogHandler{})

func init() {
	slog.SetDefault(Log)
}

type legacyLogHandler struct {
	operations []legacyLogOperation
}

type legacyLogOperation struct {
	attrs []slog.Attr
	group string
}

func (h legacyLogHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.current().Enabled(ctx, level)
}

func (h legacyLogHandler) Handle(ctx context.Context, record slog.Record) error {
	return h.current().Handle(ctx, record)
}

func (h legacyLogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	cloned := h.clone()
	cloned.operations = append(cloned.operations, legacyLogOperation{attrs: sanitizeLogAttrs(attrs)})
	return cloned
}

func (h legacyLogHandler) WithGroup(name string) slog.Handler {
	cloned := h.clone()
	cloned.operations = append(cloned.operations, legacyLogOperation{group: sanitizeLogKey(name)})
	return cloned
}

func (h legacyLogHandler) clone() legacyLogHandler {
	return legacyLogHandler{operations: append([]legacyLogOperation(nil), h.operations...)}
}

func (h legacyLogHandler) current() slog.Handler {
	handler := legacyLoggerTarget().Handler()
	for _, operation := range h.operations {
		if operation.group != "" {
			handler = handler.WithGroup(operation.group)
		} else {
			handler = handler.WithAttrs(operation.attrs)
		}
	}
	return handler
}

// ContextHandler 自动从 context 中提取 request_id 并注入日志
type ContextHandler struct {
	slog.Handler
}

// Handle 实现 slog.Handler 接口
func (h *ContextHandler) Handle(ctx context.Context, r slog.Record) error {
	r = sanitizeLogRecord(r)
	if ctx == nil {
		return h.Handler.Handle(ctx, r)
	}
	if rid, ok := ctx.Value(RequestIDKey).(string); ok {
		r.AddAttrs(sanitizeLogAttr(slog.String(string(RequestIDKey), rid)))
	}
	return h.Handler.Handle(ctx, r)
}

func (h *ContextHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &ContextHandler{Handler: h.Handler.WithAttrs(sanitizeLogAttrs(attrs))}
}

func (h *ContextHandler) WithGroup(name string) slog.Handler {
	return &ContextHandler{Handler: h.Handler.WithGroup(sanitizeLogKey(name))}
}

// SetDefaultLogger initializes the legacy global logger with default settings.
func SetDefaultLogger() {
	setDefaultLoggerForConfig(nil)
}

func setDefaultLoggerForConfig(config *SysConfig) {
	logger := newLogger(config)
	setDefaultLogger(logger)
}

func newLogger(config *SysConfig) *slog.Logger {
	level := slog.LevelInfo
	if config != nil && config.Log != nil {
		level = parseLogLevel(config.Log.Level)
	}
	opts := &slog.HandlerOptions{
		Level: level,
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
	return slog.New(handler)
}

func setDefaultLogger(logger *slog.Logger) {
	if logger == nil {
		logger = bootstrapLogger
	}
	updateDefaultFacade(func(facade legacyFacade) legacyFacade {
		facade.logger = logger
		return facade
	})
	slog.SetDefault(Log)
}

func legacyLogger() *slog.Logger {
	return Log
}

func legacyLoggerTarget() *slog.Logger {
	if facade := loadDefaultFacade(); facade != nil && facade.logger != nil {
		return facade.logger
	}
	return bootstrapLogger
}

func parseLogLevel(raw string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

func Info(msg string, args ...any) {
	legacyLogger().Info(msg, args...)
}

func ErrorLog(msg string, args ...any) {
	legacyLogger().Error(msg, args...)
}

func Warn(msg string, args ...any) {
	legacyLogger().Warn(msg, args...)
}

func Debug(msg string, args ...any) {
	legacyLogger().Debug(msg, args...)
}

func InfoContext(ctx context.Context, msg string, args ...any) {
	legacyLogger().InfoContext(ctx, msg, args...)
}

func ErrorContext(ctx context.Context, msg string, args ...any) {
	legacyLogger().ErrorContext(ctx, msg, args...)
}

func WarnContext(ctx context.Context, msg string, args ...any) {
	legacyLogger().WarnContext(ctx, msg, args...)
}

func DebugContext(ctx context.Context, msg string, args ...any) {
	legacyLogger().DebugContext(ctx, msg, args...)
}

func WithContext(ctx context.Context) *slog.Logger {
	if ctx == nil {
		return legacyLogger()
	}
	if rid, ok := ctx.Value(RequestIDKey).(string); ok {
		return legacyLogger().With(string(RequestIDKey), rid)
	}
	return legacyLogger()
}
