package bear

import (
	"context"
	"fmt"
	"time"
)

// AuthTokenManager manages token lifecycle including blacklist
type AuthTokenManager struct {
	Redis   *RedisAdapter `inject:"-"`
	JWTUtil *JWTUtil      `inject:"-"`
}

func NewAuthTokenManager() *AuthTokenManager {
	return &AuthTokenManager{}
}

func (this *AuthTokenManager) Name() string {
	return "AuthTokenManager"
}

// GenerateToken generates a new JWT token
func (this *AuthTokenManager) GenerateToken(userID uint, email string) (string, error) {
	return this.JWTUtil.GenerateToken(userID, email)
}

// ParseToken parses and validates a JWT token
func (this *AuthTokenManager) ParseToken(tokenStr string) (*CustomClaims, error) {
	// 1. Basic JWT validation
	claims, err := this.JWTUtil.ParseToken(tokenStr)
	if err != nil {
		return nil, err
	}

	// 2. Check if token is blacklisted
	isBlacklisted, err := this.IsTokenBlacklisted(context.Background(), tokenStr)
	if err != nil {
		// Log error but maybe allow or fail secure?
		// Failing secure is safer.
		return nil, fmt.Errorf("failed to check token blacklist: %v", err)
	}
	if isBlacklisted {
		return nil, NewError(401, "token is revoked")
	}

	return claims, nil
}

// RevokeToken adds the token to the blacklist
func (this *AuthTokenManager) RevokeToken(ctx context.Context, tokenStr string) error {
	// Need to parse token to get expiration time
	claims, err := this.JWTUtil.ParseToken(tokenStr)
	if err != nil {
		return err // Invalid token, no need to blacklist (or already expired)
	}

	expirationTime := claims.ExpiresAt.Time
	ttl := time.Until(expirationTime)

	if ttl <= 0 {
		return nil // Already expired
	}

	// Key: bear:auth:blacklist:<token_signature> (or full token if length permits, usually signature is enough but safer to hash full token or use full token)
	// Using full token as key might be long.
	// Let's use "bear:auth:blacklist:" + tokenStr
	key := this.blacklistKey(tokenStr)

	return this.Redis.Client.Set(ctx, key, "revoked", ttl).Err()
}

// IsTokenBlacklisted checks if the token is in the blacklist
func (this *AuthTokenManager) IsTokenBlacklisted(ctx context.Context, tokenStr string) (bool, error) {
	key := this.blacklistKey(tokenStr)
	exists, err := this.Redis.Client.Exists(ctx, key).Result()
	if err != nil {
		return false, err
	}
	return exists > 0, nil
}

func (this *AuthTokenManager) blacklistKey(token string) string {
	return fmt.Sprintf("bear:auth:blacklist:%s", token)
}
