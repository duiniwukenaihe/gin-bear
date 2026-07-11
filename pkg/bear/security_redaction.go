package bear

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"reflect"
	"strings"

	"github.com/golang-jwt/jwt/v5"
)

const redactedObservableValue = "[REDACTED]"

// SanitizeForObservability converts arbitrary values to a bounded value that
// is safe to attach to logs, traces, health output, or panic reports.
func SanitizeForObservability(value any) string {
	if value == nil {
		return "none"
	}
	if err, ok := value.(error); ok {
		category, _ := observableErrorMetadata(err, 0)
		return category
	}
	return redactedObservableValue
}

func observableErrorMetadata(err error, fallbackStatus int) (string, int) {
	code := fallbackStatus
	if err == nil {
		return "none", code
	}

	var bearError *BearError
	if errors.As(err, &bearError) && bearError != nil {
		if bearError.Code != 0 {
			code = bearError.Code
		} else if bearError.Status != 0 {
			code = bearError.Status
		}
		return "bear_error", code
	}

	switch {
	case errors.Is(err, context.Canceled):
		return "context_canceled", code
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded", code
	case errors.Is(err, jwt.ErrTokenExpired):
		return "token_expired", code
	case errors.Is(err, jwt.ErrTokenRequiredClaimMissing):
		return "token_missing_claim", code
	case errors.Is(err, jwt.ErrTokenMalformed),
		errors.Is(err, jwt.ErrTokenSignatureInvalid),
		errors.Is(err, jwt.ErrTokenInvalidClaims):
		return "token_invalid", code
	case fallbackStatus >= http.StatusBadRequest && fallbackStatus < http.StatusInternalServerError:
		return "request_error", code
	default:
		return "internal_error", code
	}
}

func sanitizeObservableString(value string) string {
	trimmed := strings.TrimSpace(value)
	if trimmed == "" {
		return value
	}
	lower := strings.ToLower(trimmed)
	if isSafeObservableCategory(lower) {
		return value
	}
	for _, marker := range []string{
		"password", "passwd", "secret", "authorization", "cookie",
		"token", "credential", "api_key", "apikey", "access_key", "dsn",
	} {
		if strings.Contains(lower, marker) {
			return redactedObservableValue
		}
	}
	if strings.HasPrefix(lower, "bearer ") || looksLikeJWT(trimmed) {
		return redactedObservableValue
	}
	if strings.Contains(trimmed, "?") || (strings.Contains(trimmed, "://") && strings.Contains(trimmed, "@")) {
		return redactedObservableValue
	}
	return value
}

func isSafeObservableCategory(value string) bool {
	switch value {
	case "none", "bear_error", "context_canceled", "deadline_exceeded",
		"token_expired", "token_missing_claim", "token_invalid",
		"request_error", "internal_error":
		return true
	default:
		return false
	}
}

func looksLikeJWT(value string) bool {
	parts := strings.Split(value, ".")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if len(part) < 2 {
			return false
		}
	}
	return true
}

func sanitizeLogRecord(record slog.Record) slog.Record {
	sanitized := slog.NewRecord(record.Time, record.Level, sanitizeObservableString(record.Message), record.PC)
	record.Attrs(func(attr slog.Attr) bool {
		sanitized.AddAttrs(sanitizeLogAttr(attr))
		return true
	})
	return sanitized
}

func sanitizeLogAttrs(attrs []slog.Attr) []slog.Attr {
	sanitized := make([]slog.Attr, 0, len(attrs))
	for _, attr := range attrs {
		sanitized = append(sanitized, sanitizeLogAttr(attr))
	}
	return sanitized
}

func sanitizeLogAttr(attr slog.Attr) slog.Attr {
	attr.Value = attr.Value.Resolve()
	if isSensitiveObservableKey(attr.Key) {
		attr.Value = slog.StringValue(redactedObservableValue)
		return attr
	}
	if isErrorObservableKey(attr.Key) {
		attr.Value = slog.StringValue(SanitizeForObservability(attr.Value.Any()))
		return attr
	}

	switch attr.Value.Kind() {
	case slog.KindString:
		attr.Value = slog.StringValue(sanitizeObservableString(attr.Value.String()))
	case slog.KindGroup:
		attr.Value = slog.GroupValue(sanitizeLogAttrs(attr.Value.Group())...)
	case slog.KindAny:
		value := attr.Value.Any()
		if err, ok := value.(error); ok {
			attr.Value = slog.StringValue(SanitizeForObservability(err))
			break
		}
		kind := reflect.TypeOf(value)
		if kind != nil && (kind.Kind() == reflect.Map || kind.Kind() == reflect.Struct || kind.Kind() == reflect.Slice || kind.Kind() == reflect.Array || kind.Kind() == reflect.Pointer) {
			attr.Value = slog.StringValue(sanitizeObservableString(fmt.Sprintf("%+v", value)))
		}
	}
	return attr
}

func isSensitiveObservableKey(key string) bool {
	normalized := strings.ToLower(strings.NewReplacer("-", "", "_", "", ".", "").Replace(key))
	for _, marker := range []string{
		"token", "password", "passwd", "secret", "authorization", "cookie",
		"credential", "apikey", "accesskey", "dsn", "query", "rawquery",
		"path", "uri", "url", "stacktrace",
	} {
		if strings.Contains(normalized, marker) {
			return true
		}
	}
	return false
}

func isErrorObservableKey(key string) bool {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "error", "err", "cause", "panic", "recovered":
		return true
	default:
		return false
	}
}
