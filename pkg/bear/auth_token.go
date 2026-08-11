package bear

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// ErrTokenRevocationUnavailable reports that no Redis revocation store exists.
var ErrTokenRevocationUnavailable = errors.New("token revocation is unavailable")

// AuthTokenManager manages token lifecycle including blacklist
type AuthTokenManager struct {
	Redis   *RedisAdapter `inject:"-"`
	JWTUtil *JWTUtil      `inject:"-"`
}

func NewAuthTokenManager() *AuthTokenManager {
	return &AuthTokenManager{}
}

func (m *AuthTokenManager) Name() string {
	return "AuthTokenManager"
}

// GenerateToken generates a new JWT token
func (m *AuthTokenManager) GenerateToken(userID uint, email string) (string, error) {
	return m.JWTUtil.GenerateToken(userID, email)
}

// ParseToken parses and validates a JWT token
func (m *AuthTokenManager) ParseToken(tokenStr string) (*CustomClaims, error) {
	return m.ParseTokenContext(context.Background(), tokenStr)
}

// ParseTokenContext parses a JWT token and checks revocation with the caller's context.
func (m *AuthTokenManager) ParseTokenContext(ctx context.Context, tokenStr string) (*CustomClaims, error) {
	// 1. Basic JWT validation
	claims, err := m.JWTUtil.ParseToken(tokenStr)
	if err != nil {
		return nil, err
	}

	if !m.revocationAvailable() {
		return claims, nil
	}

	// 2. Check if token is blacklisted
	isBlacklisted, err := m.IsTokenBlacklisted(ctx, tokenStr)
	if err != nil {
		return nil, fmt.Errorf("failed to check token blacklist: %w", err)
	}
	if isBlacklisted {
		return nil, NewError(401, "token is revoked")
	}

	return claims, nil
}

// RevokeToken adds the token to the blacklist
func (m *AuthTokenManager) RevokeToken(ctx context.Context, tokenStr string) error {
	if !m.revocationAvailable() {
		return ErrTokenRevocationUnavailable
	}
	// Need to parse token to get expiration time
	claims, err := m.JWTUtil.ParseToken(tokenStr)
	if err != nil {
		return err // Invalid token, no need to blacklist (or already expired)
	}
	if claims == nil || claims.ExpiresAt == nil {
		return jwt.ErrTokenRequiredClaimMissing
	}

	expirationTime := claims.ExpiresAt.Time
	ttl := time.Until(expirationTime.Add(m.JWTUtil.Config.ClockSkew))

	if ttl <= 0 {
		return nil // Already expired
	}

	// Key: bear:auth:blacklist:<token_signature> (or full token if length permits, usually signature is enough but safer to hash full token or use full token)
	// Using full token as key might be long.
	// Let's use "bear:auth:blacklist:" + tokenStr
	key := m.blacklistKey(tokenStr)

	return m.Redis.Client.Set(ctx, key, "revoked", ttl).Err()
}

// IsTokenBlacklisted checks if the token is in the blacklist
func (m *AuthTokenManager) IsTokenBlacklisted(ctx context.Context, tokenStr string) (bool, error) {
	if !m.revocationAvailable() {
		return false, ErrTokenRevocationUnavailable
	}
	key := m.blacklistKey(tokenStr)
	exists, err := m.Redis.Client.Exists(ctx, key).Result()
	if err != nil {
		return false, err
	}
	return exists > 0, nil
}

func (m *AuthTokenManager) revocationAvailable() bool {
	return m != nil && m.Redis != nil && m.Redis.Client != nil
}

func (m *AuthTokenManager) blacklistKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return fmt.Sprintf("bear:auth:blacklist:%s", hex.EncodeToString(sum[:]))
}
