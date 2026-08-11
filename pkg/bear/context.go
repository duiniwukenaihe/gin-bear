package bear

import (
	"context"
	"fmt"
)

type userIDContextKey struct{}
type legacyContextKey string

const (
	legacyUserIDKey  legacyContextKey = "user_id"
	legacySubjectKey legacyContextKey = "sub"
)

// WithUserID stores a user ID without relying on an untyped context key.
func WithUserID(ctx context.Context, userID string) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	return context.WithValue(ctx, userIDContextKey{}, userID)
}

// UserIDFromContext returns user IDs from the typed helper and legacy keys.
func UserIDFromContext(ctx context.Context) (string, bool) {
	if ctx == nil {
		return "", false
	}
	for _, key := range []any{userIDContextKey{}, legacyUserIDKey, legacySubjectKey, "current_user_id", "user_id", "sub"} {
		if userID, ok := normalizeUserID(ctx.Value(key)); ok {
			return userID, true
		}
	}
	return "", false
}

func normalizeUserID(value any) (string, bool) {
	if value == nil {
		return "", false
	}
	if userID, ok := value.(string); ok {
		return userID, userID != ""
	}
	return fmt.Sprint(value), true
}
