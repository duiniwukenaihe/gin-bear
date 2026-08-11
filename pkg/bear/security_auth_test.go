package bear

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
	"github.com/redis/go-redis/v9"
)

func TestJWTTokenSizeRejectsTokenOver16KiB(t *testing.T) {
	if maxJWTTokenBytes != 16<<10 {
		t.Fatalf("maxJWTTokenBytes = %d, want %d", maxJWTTokenBytes, 16<<10)
	}
	util := NewJWTUtil("security-test-secret", 1)
	token, err := util.GenerateToken(1, strings.Repeat("x", maxJWTTokenBytes))
	if err != nil {
		t.Fatal(err)
	}
	if len(token) <= maxJWTTokenBytes {
		t.Fatalf("generated token size = %d, want more than %d", len(token), maxJWTTokenBytes)
	}

	if _, err := util.ParseToken(token); err == nil {
		t.Fatal("ParseToken accepted a token larger than 16 KiB")
	}
}

func TestParseTokenRequiresExpirationClaim(t *testing.T) {
	util := NewJWTUtil("security-test-secret", 1)
	token := signedJWT(t, util.Config.Secret, jwt.RegisteredClaims{
		IssuedAt: jwt.NewNumericDate(time.Now()),
	})

	_, err := util.ParseToken(token)
	if !errors.Is(err, jwt.ErrTokenRequiredClaimMissing) {
		t.Fatalf("ParseToken error = %v, want missing required claim", err)
	}
}

func TestGenerateTokenRejectsNonPositiveExpiration(t *testing.T) {
	for _, expires := range []int{0, -1} {
		util := NewJWTUtil("security-test-secret", expires)
		if _, err := util.GenerateToken(1, "user@example.com"); err == nil {
			t.Fatalf("GenerateToken expires=%d returned no error", expires)
		}
	}
}

func TestParseTokenRejectsNonPositiveConfiguredExpiration(t *testing.T) {
	util := NewJWTUtil("security-test-secret", 1)
	token, err := util.GenerateToken(1, "user@example.com")
	if err != nil {
		t.Fatal(err)
	}
	util.Config.Expires = 0

	if _, err := util.ParseToken(token); !errors.Is(err, ErrInvalidTokenExpiration) {
		t.Fatalf("ParseToken error = %v, want invalid configured expiration", err)
	}
}

func TestRevokeTokenWithoutExpirationReturnsErrorWithoutPanic(t *testing.T) {
	manager, _ := newSecurityTokenManager(t, time.Minute)
	token := signedJWT(t, manager.JWTUtil.Config.Secret, jwt.RegisteredClaims{
		IssuedAt: jwt.NewNumericDate(time.Now()),
	})

	defer func() {
		if recovered := recover(); recovered != nil {
			t.Fatalf("RevokeToken panicked: %v", recovered)
		}
	}()
	if err := manager.RevokeToken(context.Background(), token); !errors.Is(err, jwt.ErrTokenRequiredClaimMissing) {
		t.Fatalf("RevokeToken error = %v, want missing required claim", err)
	}
}

func TestRevokeTokenKeepsBlacklistThroughClockSkewWindow(t *testing.T) {
	manager, server := newSecurityTokenManager(t, time.Minute)
	expiresAt := time.Now().Add(-5 * time.Second).Truncate(time.Second)
	token := signedJWT(t, manager.JWTUtil.Config.Secret, jwt.RegisteredClaims{
		ExpiresAt: jwt.NewNumericDate(expiresAt),
		IssuedAt:  jwt.NewNumericDate(time.Now().Add(-time.Hour)),
	})

	if err := manager.RevokeToken(context.Background(), token); err != nil {
		t.Fatalf("RevokeToken error = %v", err)
	}
	key := manager.blacklistKey(token)
	ttl := server.TTL(key)
	if ttl < 50*time.Second || ttl > time.Minute {
		t.Fatalf("blacklist TTL = %s, want remaining exp + clock skew window", ttl)
	}
	if _, err := manager.ParseToken(token); err == nil {
		t.Fatal("ParseToken accepted token revoked inside clock skew window")
	}
}

func TestRevokeTokenRejectsExpiredBeyondClockSkew(t *testing.T) {
	manager, server := newSecurityTokenManager(t, 5*time.Second)
	token := signedJWT(t, manager.JWTUtil.Config.Secret, jwt.RegisteredClaims{
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(-time.Minute)),
	})

	err := manager.RevokeToken(context.Background(), token)
	if !errors.Is(err, jwt.ErrTokenExpired) {
		t.Fatalf("RevokeToken error = %v, want expired token", err)
	}
	if server.Exists(manager.blacklistKey(token)) {
		t.Fatal("expired token was added to blacklist")
	}
}

func TestRevokeTokenFailsWhenRedisWriteFails(t *testing.T) {
	manager, server := newSecurityTokenManager(t, 0)
	token, err := manager.GenerateToken(1, "user@example.com")
	if err != nil {
		t.Fatal(err)
	}
	server.Close()

	if err := manager.RevokeToken(context.Background(), token); err == nil {
		t.Fatal("RevokeToken returned no error after Redis failure")
	}
}

func TestAuthFairingUsesRequestContextForBlacklist(t *testing.T) {
	manager, _ := newSecurityTokenManager(t, 0)
	token, err := manager.GenerateToken(1, "user@example.com")
	if err != nil {
		t.Fatal(err)
	}

	requestContext, cancel := context.WithCancel(context.Background())
	cancel()
	request := httptest.NewRequest(http.MethodGet, "/private", nil).WithContext(requestContext)
	request.Header.Set("Authorization", "Bearer "+token)
	ctx, _ := gin.CreateTestContext(httptest.NewRecorder())
	ctx.Request = request
	cfg := NewSysConfig()
	cfg.Auth.PublicPaths = nil
	ctx.Set(runtimeContextKey, &Runtime{Config: cfg})

	err = (&AuthFairing{TokenManager: manager}).OnRequest(ctx)
	if err == nil {
		t.Fatal("AuthFairing accepted a token after the request context was canceled")
	}
	var bearErr *BearError
	if !errors.As(err, &bearErr) || bearErr.Code != http.StatusUnauthorized {
		t.Fatalf("AuthFairing error = %v, want 401", err)
	}
}

func TestParseTokenContextPropagatesCanceledRedisContext(t *testing.T) {
	manager, _ := newSecurityTokenManager(t, 0)
	token, err := manager.GenerateToken(1, "user@example.com")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = manager.ParseTokenContext(ctx, token)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("ParseTokenContext error = %v, want context.Canceled", err)
	}
}

func newSecurityTokenManager(t *testing.T, skew time.Duration) (*AuthTokenManager, *miniredis.Miniredis) {
	t.Helper()
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{
		Addr:         server.Addr(),
		MaxRetries:   -1,
		DialTimeout:  100 * time.Millisecond,
		ReadTimeout:  100 * time.Millisecond,
		WriteTimeout: 100 * time.Millisecond,
	})
	t.Cleanup(func() { _ = client.Close() })
	util := NewJWTUtil("security-test-secret", 1)
	util.Config.ClockSkew = skew
	return &AuthTokenManager{
		JWTUtil: util,
		Redis:   &RedisAdapter{Client: client},
	}, server
}
