package bear

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"sync"
)

var Log *slog.Logger
var logMu sync.RWMutex

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
func SetDefaultLogger(config ...*SysConfig) {
	var cfg *SysConfig
	if len(config) > 0 {
		cfg = config[0]
	}
	logger := newLogger(cfg)
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
		logger = slog.Default()
	}
	logMu.Lock()
	Log = logger
	logMu.Unlock()
	slog.SetDefault(logger)
}

func legacyLogger() *slog.Logger {
	logMu.RLock()
	logger := Log
	logMu.RUnlock()
	if logger != nil {
		return logger
	}
	return slog.Default()
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
