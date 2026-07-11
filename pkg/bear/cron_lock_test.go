package bear

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/redis/go-redis/v9"
)

func TestCronLockCannotDeleteNewOwner(t *testing.T) {
	redisServer := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: redisServer.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	first := newCronLock(client, "jobs:billing", "owner-a", time.Second)
	second := newCronLock(client, "jobs:billing", "owner-b", time.Second)
	requireNoError(t, first.Acquire(context.Background()))
	redisServer.FastForward(2 * time.Second)
	requireNoError(t, second.Acquire(context.Background()))
	requireNoError(t, first.Release(context.Background()))
	assertLockOwner(t, client, "jobs:billing", "owner-b")
}

func TestBuildDSNPostgresEscapesReservedCredentialsAndDatabaseName(t *testing.T) {
	cfg := &DBConfig{
		Type:            "postgres",
		Host:            "2001:db8::1",
		Port:            "5432",
		User:            "billing user@example.com",
		Password:        "p@ss word:/?#[]",
		DBName:          "billing/ledger?2026",
		PostgresSSLMode: "verify-full",
	}
	dsn, err := buildDSN(cfg)
	requireNoError(t, err)
	parsed, err := url.Parse(dsn)
	requireNoError(t, err)
	password, _ := parsed.User.Password()
	if parsed.User.Username() != cfg.User || password != cfg.Password || strings.TrimPrefix(parsed.Path, "/") != cfg.DBName {
		t.Fatalf("PostgreSQL DSN did not round-trip: %s", dsn)
	}
	if got := parsed.Query().Get("sslmode"); got != "verify-full" {
		t.Fatalf("sslmode = %q", got)
	}
	if got := parsed.Host; got != "[2001:db8::1]:5432" {
		t.Fatalf("host = %q", got)
	}
}

func TestBuildDSNPostgresValidatesSSLModeAndSupportsLegacyInput(t *testing.T) {
	legacy := &DBConfig{Type: "postgres", SSLMode: "require"}
	dsn, err := buildDSN(legacy)
	requireNoError(t, err)
	parsed, err := url.Parse(dsn)
	requireNoError(t, err)
	if got := parsed.Query().Get("sslmode"); got != "require" {
		t.Fatalf("legacy sslmode = %q", got)
	}

	invalid := &DBConfig{Type: "postgres", PostgresSSLMode: "sometimes"}
	if _, err := buildDSN(invalid); err == nil || !strings.Contains(err.Error(), "sslmode") {
		t.Fatalf("buildDSN() error = %v", err)
	}
}

func TestBuildDSNMySQLUsesDriverConfigTLSAndReservedValues(t *testing.T) {
	cfg := &DBConfig{
		Type:     "mysql",
		Host:     "localhost",
		Port:     "3306",
		User:     "billing@example.com",
		Password: "p@ss:word/?#[]",
		DBName:   "billing ledger",
		TLS:      "skip-verify",
	}
	dsn, err := buildDSN(cfg)
	requireNoError(t, err)
	parsed, err := mysqldriver.ParseDSN(dsn)
	requireNoError(t, err)
	if parsed.User != cfg.User || parsed.Passwd != cfg.Password || parsed.DBName != cfg.DBName {
		t.Fatalf("MySQL DSN did not round-trip: %#v", parsed)
	}
	if parsed.TLSConfig != cfg.TLS || !parsed.ParseTime || parsed.Params["charset"] != "utf8mb4" {
		t.Fatalf("MySQL driver settings = %#v", parsed)
	}
	if strings.Contains(dsn, "sslmode=") {
		t.Fatalf("MySQL DSN contains PostgreSQL sslmode: %s", dsn)
	}
}

func TestBuildDSNMySQLIgnoresLegacySSLModeAndWarns(t *testing.T) {
	cfg := NewSysConfig()
	cfg.DB.Type = "mysql"
	cfg.DB.SSLMode = "require"
	cfg.DB.TLS = ""
	dsn, err := buildDSN(cfg.DB)
	requireNoError(t, err)
	if strings.Contains(dsn, "sslmode=") || strings.Contains(dsn, "tls=") {
		t.Fatalf("legacy sslmode leaked into MySQL DSN: %s", dsn)
	}
	warnings := cfg.compatibilityWarnings()
	if len(warnings) != 1 || !strings.Contains(warnings[0], "database.tls") {
		t.Fatalf("migration warnings = %q", warnings)
	}
}

func TestRedisOpenAdapterReturnsPingErrors(t *testing.T) {
	addr := unavailableTCPAddress(t)
	adapter, err := OpenRedisAdapter(&RedisConfig{
		Addr:        addr,
		DialTimeout: 1,
		ReadTimeout: 1,
	})
	if err == nil {
		t.Fatal("OpenRedisAdapter() unexpectedly succeeded")
	}
	if adapter != nil {
		t.Fatal("OpenRedisAdapter() must not return an adapter after a failed ping")
	}
}

func TestRedisRateLimiterRoundsSubMillisecondWindowUpToOneMillisecond(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	limiter := NewRedisRateLimiter(&RedisAdapter{Client: client}, 1, time.Nanosecond)
	if err := limiter.Validate(); err != nil {
		t.Fatalf("sub-millisecond window was rejected: %v", err)
	}
	if !limiter.Allow(context.Background(), "client") {
		t.Fatal("Redis rate limiter denied the first request")
	}

	ttl, err := client.PTTL(context.Background(), "bear_limiter:client").Result()
	if err != nil {
		t.Fatalf("read limiter TTL: %v", err)
	}
	if ttl <= 0 {
		t.Fatalf("accepted limiter window produced TTL %s", ttl)
	}
}

func TestRedisOpenAdapterPingsBeforeReturningSuccess(t *testing.T) {
	server := miniredis.RunT(t)
	adapter, err := OpenRedisAdapter(&RedisConfig{Addr: server.Addr()})
	requireNoError(t, err)
	if adapter == nil || adapter.Client == nil {
		t.Fatal("OpenRedisAdapter() returned a nil adapter")
	}
	t.Cleanup(func() { _ = adapter.Shutdown() })
	requireNoError(t, adapter.CheckReady(context.Background()))
}

func TestRedisCompatibilityWrapperKeepsOptionalStartupBehavior(t *testing.T) {
	adapter := NewRedisAdapter(&RedisConfig{
		Addr:        unavailableTCPAddress(t),
		DialTimeout: 1,
		ReadTimeout: 1,
	})
	if adapter == nil || adapter.Client == nil {
		t.Fatal("NewRedisAdapter() returned nil for optional Redis")
	}
	_ = adapter.Shutdown()
}

func TestRateLimiterNamedFailureModeAndLegacyOverride(t *testing.T) {
	openLimiter := NewRedisRateLimiter(nil, 1, time.Second)
	openLimiter.FailureMode = LimiterFailureModeOpen
	if !openLimiter.Allow(context.Background(), "client") {
		t.Fatal("open failure mode denied a request")
	}

	closedLimiter := NewRedisRateLimiter(nil, 1, time.Second)
	closedLimiter.FailureMode = LimiterFailureModeClosed
	if closedLimiter.Allow(context.Background(), "client") {
		t.Fatal("closed failure mode allowed a request")
	}

	legacyLimiter := NewRedisRateLimiter(nil, 1, time.Second)
	legacyLimiter.FailureMode = LimiterFailureModeOpen
	legacyLimiter.FailClosed = true
	if legacyLimiter.Allow(context.Background(), "client") {
		t.Fatal("legacy FailClosed must override open failure mode")
	}
}

func TestRateLimiterValidatesPositiveLimitWindowAndFailureMode(t *testing.T) {
	memory := NewMemoryRateLimiter(0, 0)
	defer memory.Stop()
	if err := memory.Validate(); err == nil {
		t.Fatal("memory limiter accepted non-positive limit and window")
	}
	if memory.Allow(context.Background(), "client") {
		t.Fatal("invalid memory limiter allowed a request")
	}

	redisLimiter := NewRedisRateLimiter(nil, -1, -time.Second)
	redisLimiter.FailureMode = LimiterFailureMode("typo")
	if err := redisLimiter.Validate(); err == nil {
		t.Fatal("Redis limiter accepted invalid policy")
	}
	if redisLimiter.Allow(context.Background(), "client") {
		t.Fatal("invalid Redis limiter allowed a request")
	}
}

func TestRateLimiterMiddlewareIncludesRetryAfter(t *testing.T) {
	limiter := NewMemoryRateLimiter(0, 1500*time.Millisecond)
	defer limiter.Stop()

	router := gin.New()
	router.Use(RateLimitMiddleware(limiter))
	router.GET("/limited", func(c *gin.Context) { c.Status(http.StatusOK) })
	response := httptest.NewRecorder()
	router.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/limited", nil))

	if response.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d", response.Code)
	}
	if got := response.Header().Get("Retry-After"); got != "2" {
		t.Fatalf("Retry-After = %q, want 2", got)
	}
}

func TestUserIDFromContextNeverPanicsOnNonStringValue(t *testing.T) {
	ctx := context.WithValue(context.Background(), legacyUserIDKey, uint(42))
	if got, ok := UserIDFromContext(ctx); !ok || got != "42" {
		t.Fatalf("got %q, %v", got, ok)
	}
}

func TestUserIDHelpersPreserveTypedAndLegacyContextValues(t *testing.T) {
	typed := WithUserID(context.Background(), "typed-user")
	if got, ok := UserIDFromContext(typed); !ok || got != "typed-user" {
		t.Fatalf("typed user ID = %q, %v", got, ok)
	}

	legacy := context.WithValue(context.Background(), legacySubjectKey, int64(7))
	if got := getUserIDFromContext(legacy); got != "7" {
		t.Fatalf("legacy user ID = %q, want 7", got)
	}
}

func TestCronRejectsNonPositiveLockTTL(t *testing.T) {
	manager := NewCronManager(nil)
	for _, ttl := range []time.Duration{0, -time.Second} {
		if _, err := manager.AddDistributedFunc("* * * * * *", "sync", ttl, func() {}); err == nil {
			t.Fatalf("AddDistributedFunc() accepted TTL %s", ttl)
		}
	}
}

func TestCronUsesUniqueOwnerTokensForEachExecution(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	manager := NewCronManager(&RedisAdapter{Client: client})
	owners := make([]string, 0, 2)
	id, err := manager.AddDistributedFunc("* * * * * *", "owners", time.Minute, func() {
		owner, getErr := client.Get(context.Background(), "bear:cron:lock:owners").Result()
		if getErr != nil {
			t.Errorf("read lock owner: %v", getErr)
			return
		}
		owners = append(owners, owner)
	})
	requireNoError(t, err)

	runCronEntry(t, manager, id)
	runCronEntry(t, manager, id)
	if len(owners) != 2 {
		t.Fatalf("owner count = %d, want 2", len(owners))
	}
	if owners[0] == "" || owners[0] == "locked" || owners[0] == owners[1] {
		t.Fatalf("owners are not unique random tokens: %q", owners)
	}
}

func TestCronLockReportsContention(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	first := newCronLock(client, "jobs:sync", "owner-a", time.Minute)
	second := newCronLock(client, "jobs:sync", "owner-b", time.Minute)
	requireNoError(t, first.Acquire(context.Background()))
	if err := second.Acquire(context.Background()); !errors.Is(err, ErrCronLockHeld) {
		t.Fatalf("Acquire() error = %v, want ErrCronLockHeld", err)
	}
}

func TestCronWarnsWhenExecutionReachesEightyPercentOfTTL(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	var logs bytes.Buffer
	manager := NewCronManager(&RedisAdapter{Client: client})
	manager.logger = slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	id, err := manager.AddDistributedFunc("* * * * * *", "slow", 100*time.Millisecond, func() {
		time.Sleep(90 * time.Millisecond)
	})
	requireNoError(t, err)

	runCronEntry(t, manager, id)
	if got := logs.String(); !strings.Contains(got, "Distributed job lock TTL nearing expiration") {
		t.Fatalf("missing 80%% TTL warning in logs: %s", got)
	}
}

func TestCronLockWarningDelayAvoidsDurationOverflow(t *testing.T) {
	maxTTL := time.Duration(1<<63 - 1)
	want := maxTTL/5*4 + maxTTL%5*4/5

	got := cronLockWarningDelay(maxTTL)
	if got <= 0 {
		t.Fatalf("warning delay = %s, want positive", got)
	}
	if got != want {
		t.Fatalf("warning delay = %s, want %s", got, want)
	}
}

func assertLockOwner(t *testing.T, client *redis.Client, key, owner string) {
	t.Helper()
	got, err := client.Get(context.Background(), key).Result()
	if err != nil {
		t.Fatalf("get lock owner: %v", err)
	}
	if got != owner {
		t.Fatalf("lock owner = %q, want %q", got, owner)
	}
}

func unavailableTCPAddress(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	requireNoError(t, err)
	addr := listener.Addr().String()
	requireNoError(t, listener.Close())
	return addr
}
