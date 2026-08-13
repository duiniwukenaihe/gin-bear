package bear

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alicebob/miniredis/v2"
	"github.com/gin-gonic/gin"
	"github.com/redis/go-redis/v9"
)

type pluginRouteAuthModule struct {
	auth *AuthFairing
}

type authRuntimeFailingModule struct{}

type authRuntimeCompatibilityRedisModule struct {
	auth *AuthFairing
}

type authRuntimeCompatibilityAttachModule struct {
	auth *AuthFairing
}

type authRuntimeBlockingCompatibilityModule struct {
	entered chan struct{}
	release chan struct{}
}

type nativeControllerAuthModule struct {
	controller IClass
}

type nativeControllerAuth struct {
	auth *AuthFairing
}

type authRuntimeProbeFairing struct {
	BaseFairing
	calls int
}

func (f *authRuntimeProbeFairing) Name() string { return "auth-runtime-probe" }
func (f *authRuntimeProbeFairing) OnRequest(*gin.Context) error {
	f.calls++
	return nil
}

func abortBeforeAuthMiddleware(status int) gin.HandlerFunc {
	return func(ctx *gin.Context) {
		ctx.AbortWithStatus(status)
	}
}

func (*pluginRouteAuthModule) Name() string  { return "plugin-route-auth" }
func (*pluginRouteAuthModule) Beans() []Bean { return nil }
func (*pluginRouteAuthModule) Build(*Bear)   {}
func (m *pluginRouteAuthModule) BuildE(app *Bear) error {
	return app.HandleWithFairingE(http.MethodGet, "/plugin-private", func() string { return "ok" }, m.auth)
}

func (*authRuntimeFailingModule) Name() string  { return "auth-runtime-failing" }
func (*authRuntimeFailingModule) Beans() []Bean { return nil }
func (*authRuntimeFailingModule) Build(*Bear)   {}
func (*authRuntimeFailingModule) BuildE(*Bear) error {
	return errors.New("forced module build failure")
}

func (*authRuntimeCompatibilityRedisModule) Name() string  { return "auth-runtime-compatibility-redis" }
func (*authRuntimeCompatibilityRedisModule) Beans() []Bean { return nil }
func (m *authRuntimeCompatibilityRedisModule) Build(app *Bear) {
	app.HandleWithFairing(http.MethodGet, "/compatibility-redis", func() string { return "ok" }, m.auth)
}

func (*authRuntimeCompatibilityAttachModule) Name() string {
	return "auth-runtime-compatibility-attach"
}
func (*authRuntimeCompatibilityAttachModule) Beans() []Bean { return nil }
func (m *authRuntimeCompatibilityAttachModule) Build(app *Bear) {
	app.Attach(m.auth)
	app.GET("/compatibility-build-attach", func(ctx *gin.Context) { ctx.Status(http.StatusNoContent) })
}

func (*authRuntimeBlockingCompatibilityModule) Name() string {
	return "auth-runtime-blocking-compatibility"
}
func (*authRuntimeBlockingCompatibilityModule) Beans() []Bean { return nil }
func (m *authRuntimeBlockingCompatibilityModule) Build(*Bear) {
	close(m.entered)
	<-m.release
}

func (*nativeControllerAuthModule) Name() string  { return "native-controller-auth-module" }
func (*nativeControllerAuthModule) Beans() []Bean { return nil }
func (m *nativeControllerAuthModule) Build(app *Bear) {
	app.Group("/native-controller", m.controller)
}

func (*nativeControllerAuth) Name() string { return "native-controller-auth" }
func (c *nativeControllerAuth) Interceptors() []Fairing {
	return []Fairing{c.auth}
}
func (*nativeControllerAuth) Build(app *Bear) {
	app.GET("/private", func(ctx *gin.Context) { ctx.Status(http.StatusNoContent) })
}

func TestAuthEnabledExplicitAuthFairingReplacesAutomaticPolicy(t *testing.T) {
	resetGinModeForTest(t)
	config := NewSysConfig()
	config.SetFrameworkStrict(true)
	config.Auth.Enabled = true
	config.Auth.JWTSecret = "automatic-auth-secret-1234567890"
	config.Auth.PublicPaths = nil

	app, err := IgniteE(config)
	if err != nil {
		t.Fatalf("IgniteE() error = %v", err)
	}
	explicitJWT := NewJWTUtil("explicit-auth-secret-1234567890", 1)
	explicit := &AuthFairing{JWTUtil: explicitJWT}
	if err := app.AttachE(explicit); err != nil {
		t.Fatalf("AttachE() error = %v", err)
	}
	app.GET("/private", func(ctx *gin.Context) { ctx.Status(http.StatusNoContent) })
	if err := app.ApplyAll(context.Background()); err != nil {
		t.Fatalf("ApplyAll() error = %v", err)
	}

	explicitToken, err := explicitJWT.GenerateToken(42, "explicit@example.com")
	if err != nil {
		t.Fatalf("generate explicit token: %v", err)
	}
	explicitResponse := authenticatedRequest(app, http.MethodGet, "/private", explicitToken)
	if explicitResponse.Code != http.StatusNoContent {
		t.Fatalf("explicit policy status = %d, want %d; body=%s", explicitResponse.Code, http.StatusNoContent, explicitResponse.Body.String())
	}

	automaticJWT := NewJWTUtil(config.Auth.JWTSecret, 1)
	automaticToken, err := automaticJWT.GenerateToken(7, "automatic@example.com")
	if err != nil {
		t.Fatalf("generate automatic token: %v", err)
	}
	automaticResponse := authenticatedRequest(app, http.MethodGet, "/private", automaticToken)
	if automaticResponse.Code != http.StatusUnauthorized {
		t.Fatalf("replaced automatic policy status = %d, want %d", automaticResponse.Code, http.StatusUnauthorized)
	}
}

func TestAuthEnabledExplicitAuthFairingReplacesAutomaticPolicyInCompatibilityMode(t *testing.T) {
	resetGinModeForTest(t)
	config := NewSysConfig()
	config.Auth.Enabled = true
	config.Auth.JWTSecret = "automatic-auth-secret-1234567890"
	config.Auth.PublicPaths = nil

	app, err := IgniteE(config)
	if err != nil {
		t.Fatalf("IgniteE() error = %v", err)
	}
	explicitJWT := NewJWTUtil("explicit-auth-secret-1234567890", 1)
	if err := app.AttachE(&AuthFairing{JWTUtil: explicitJWT}); err != nil {
		t.Fatalf("AttachE() error = %v", err)
	}
	app.GET("/private", func(ctx *gin.Context) { ctx.Status(http.StatusNoContent) })
	if err := app.ApplyAll(context.Background()); err != nil {
		t.Fatalf("ApplyAll() error = %v", err)
	}

	token, err := explicitJWT.GenerateToken(42, "explicit@example.com")
	if err != nil {
		t.Fatalf("generate explicit token: %v", err)
	}
	response := authenticatedRequest(app, http.MethodGet, "/private", token)
	if response.Code != http.StatusNoContent {
		t.Fatalf("explicit policy status = %d, want %d; body=%s", response.Code, http.StatusNoContent, response.Body.String())
	}
}

func TestAttachEDuplicateExplicitAuthFairingReturnsError(t *testing.T) {
	resetGinModeForTest(t)
	config := NewSysConfig()
	config.SetFrameworkStrict(true)
	app, err := IgniteE(config)
	if err != nil {
		t.Fatalf("IgniteE() error = %v", err)
	}
	if err := app.AttachE(NewAuthFairing()); err != nil {
		t.Fatalf("first AttachE() error = %v", err)
	}

	err = app.AttachE(NewAuthFairing())
	if !errors.Is(err, ErrBeanDuplicate) {
		t.Fatalf("second AttachE() error = %v, want ErrBeanDuplicate", err)
	}
}

func TestCompatibilityAttachRetainsFirstExplicitAuthFairing(t *testing.T) {
	resetGinModeForTest(t)
	config := NewSysConfig()
	config.Auth.Enabled = true
	config.Auth.JWTSecret = "automatic-auth-secret-1234567890"
	config.Auth.PublicPaths = nil
	app, err := IgniteE(config)
	if err != nil {
		t.Fatalf("IgniteE() error = %v", err)
	}

	firstJWT := NewJWTUtil("first-explicit-secret-1234567890", 1)
	secondJWT := NewJWTUtil("second-explicit-secret-1234567890", 1)
	app.Attach(&AuthFairing{JWTUtil: firstJWT})
	probe := &authRuntimeProbeFairing{}
	app.Attach(&AuthFairing{JWTUtil: secondJWT}, probe)
	if err := app.HandleE(http.MethodGet, "/private", func() StatusResponse {
		return StatusResponse{Status: http.StatusNoContent}
	}); err != nil {
		t.Fatalf("HandleE() error = %v", err)
	}
	if err := app.ApplyAll(context.Background()); err != nil {
		t.Fatalf("ApplyAll() error = %v", err)
	}

	firstToken, err := firstJWT.GenerateToken(1, "first@example.com")
	if err != nil {
		t.Fatalf("generate first token: %v", err)
	}
	if response := authenticatedRequest(app, http.MethodGet, "/private", firstToken); response.Code != http.StatusNoContent {
		t.Fatalf("first explicit policy status = %d, want %d; body=%s", response.Code, http.StatusNoContent, response.Body.String())
	}
	if probe.calls != 1 {
		t.Fatalf("non-conflicting fairing calls = %d, want 1", probe.calls)
	}
	secondToken, err := secondJWT.GenerateToken(2, "second@example.com")
	if err != nil {
		t.Fatalf("generate second token: %v", err)
	}
	if response := authenticatedRequest(app, http.MethodGet, "/private", secondToken); response.Code != http.StatusUnauthorized {
		t.Fatalf("ignored second policy status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}

func TestCompatibilityAttachAfterStartupCannotReplaceAuthenticationPolicy(t *testing.T) {
	resetGinModeForTest(t)
	config := NewSysConfig()
	config.Auth.Enabled = true
	config.Auth.JWTSecret = "startup-auth-secret-1234567890"
	config.Auth.PublicPaths = nil
	app, err := IgniteE(config)
	if err != nil {
		t.Fatalf("IgniteE() error = %v", err)
	}
	app.GET("/private", func(ctx *gin.Context) { ctx.Status(http.StatusNoContent) })
	if err := app.ApplyAll(context.Background()); err != nil {
		t.Fatalf("ApplyAll() error = %v", err)
	}
	t.Cleanup(func() { _ = app.Shutdown(context.Background()) })

	lateJWT := NewJWTUtil("late-auth-secret-1234567890", 1)
	app.Attach(&AuthFairing{JWTUtil: lateJWT})

	startupJWT := NewJWTUtil(config.Auth.JWTSecret, 1)
	startupToken, err := startupJWT.GenerateToken(1, "startup@example.com")
	if err != nil {
		t.Fatalf("generate startup token: %v", err)
	}
	if response := authenticatedRequest(app, http.MethodGet, "/private", startupToken); response.Code != http.StatusNoContent {
		t.Fatalf("startup policy status = %d, want %d; body=%s", response.Code, http.StatusNoContent, response.Body.String())
	}

	lateToken, err := lateJWT.GenerateToken(2, "late@example.com")
	if err != nil {
		t.Fatalf("generate late token: %v", err)
	}
	if response := authenticatedRequest(app, http.MethodGet, "/private", lateToken); response.Code != http.StatusUnauthorized {
		t.Fatalf("late policy status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}

func TestCompatibilityModuleBuildCannotAttachAuthenticationPolicyAfterLifecycleStarts(t *testing.T) {
	resetGinModeForTest(t)
	config := NewSysConfig()
	config.Auth.Enabled = false
	config.Auth.PublicPaths = nil
	jwtUtil := NewJWTUtil("compatibility-build-secret-1234567890", 1)
	app := Ignite(config)
	app.AddModule(&authRuntimeCompatibilityAttachModule{auth: &AuthFairing{JWTUtil: jwtUtil}})
	err := app.ApplyAll(context.Background())
	if !errors.Is(err, ErrLifecycleRegistrationClosed) {
		t.Fatalf("ApplyAll() error = %v, want ErrLifecycleRegistrationClosed", err)
	}
}

func TestConcurrentCompatibilityAttachDuringBuildFailsStartup(t *testing.T) {
	resetGinModeForTest(t)
	config := NewSysConfig()
	config.Auth.Enabled = true
	config.Auth.JWTSecret = "startup-concurrent-secret-1234567890"
	module := &authRuntimeBlockingCompatibilityModule{
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	app := Ignite(config)
	app.AddModule(module)
	result := make(chan error, 1)
	go func() { result <- app.ApplyAll(context.Background()) }()
	<-module.entered
	app.Attach(&AuthFairing{JWTUtil: NewJWTUtil("concurrent-late-secret-1234567890", 1)})
	close(module.release)

	if err := <-result; !errors.Is(err, ErrLifecycleRegistrationClosed) {
		t.Fatalf("ApplyAll() error = %v, want ErrLifecycleRegistrationClosed", err)
	}
}

func TestExplicitAuthTokenManagerBeanDrivesExplicitPolicy(t *testing.T) {
	resetGinModeForTest(t)
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	config := NewSysConfig()
	config.SetFrameworkStrict(true)
	config.Auth.Enabled = true
	config.Auth.JWTSecret = "automatic-auth-secret-1234567890"
	config.Auth.PublicPaths = nil
	app, err := IgniteE(config)
	if err != nil {
		t.Fatalf("IgniteE() error = %v", err)
	}
	explicitJWT := NewJWTUtil("explicit-manager-secret-1234567890", 1)
	manager := &AuthTokenManager{
		JWTUtil: explicitJWT,
		Redis:   &RedisAdapter{Client: client},
	}
	if err := app.BeansE(manager); err != nil {
		t.Fatalf("BeansE(explicit AuthTokenManager) error = %v", err)
	}
	if err := app.AttachE(&AuthFairing{TokenManager: manager}); err != nil {
		t.Fatalf("AttachE(explicit policy) error = %v", err)
	}
	app.GET("/private", func(ctx *gin.Context) { ctx.Status(http.StatusNoContent) })
	if err := app.ApplyAll(context.Background()); err != nil {
		t.Fatalf("ApplyAll() error = %v", err)
	}

	token, err := explicitJWT.GenerateToken(11, "manager@example.com")
	if err != nil {
		t.Fatalf("generate explicit manager token: %v", err)
	}
	if response := authenticatedRequest(app, http.MethodGet, "/private", token); response.Code != http.StatusNoContent {
		t.Fatalf("explicit manager token status = %d, want %d; body=%s", response.Code, http.StatusNoContent, response.Body.String())
	}
	if err := manager.RevokeToken(context.Background(), token); err != nil {
		t.Fatalf("revoke explicit manager token: %v", err)
	}
	if response := authenticatedRequest(app, http.MethodGet, "/private", token); response.Code != http.StatusUnauthorized {
		t.Fatalf("revoked explicit manager token status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}

func TestAutomaticAuthUsesRedisTokenManagerWhenAdapterBeanExists(t *testing.T) {
	resetGinModeForTest(t)
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() { _ = client.Close() })

	config := NewSysConfig()
	config.SetFrameworkStrict(true)
	config.Auth.Enabled = true
	config.Auth.StorageType = "redis"
	config.Auth.JWTSecret = "redis-auth-secret-1234567890"
	config.Auth.PublicPaths = nil
	app, err := IgniteE(config)
	if err != nil {
		t.Fatalf("IgniteE() error = %v", err)
	}
	if err := app.BeansE(&RedisAdapter{Client: client}); err != nil {
		t.Fatalf("BeansE(RedisAdapter) error = %v", err)
	}
	app.GET("/private", func(ctx *gin.Context) { ctx.Status(http.StatusNoContent) })
	if err := app.ApplyAll(context.Background()); err != nil {
		t.Fatalf("ApplyAll() error = %v", err)
	}

	jwtUtil := NewJWTUtil(config.Auth.JWTSecret, 1)
	token, err := jwtUtil.GenerateToken(9, "revoked@example.com")
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	manager := &AuthTokenManager{JWTUtil: jwtUtil, Redis: &RedisAdapter{Client: client}}
	if err := manager.RevokeToken(context.Background(), token); err != nil {
		t.Fatalf("revoke token: %v", err)
	}

	response := authenticatedRequest(app, http.MethodGet, "/private", token)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("revoked token status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}

func TestProductionRedisAuthOpensConfiguredAdapterAtStartup(t *testing.T) {
	resetGinModeForTest(t)
	server := miniredis.RunT(t)

	config := NewSysConfig()
	config.Server.Mode = gin.ReleaseMode
	config.SetFrameworkStrict(true)
	config.Auth.Enabled = true
	config.Auth.StorageType = "redis"
	config.Auth.JWTSecret = randomProductionJWTKey(t)
	config.Auth.PublicPaths = nil
	config.Redis.Addr = server.Addr()
	app, err := IgniteE(config)
	if err != nil {
		t.Fatalf("IgniteE() error = %v", err)
	}
	if err := app.EnableRedisE(context.Background()); err != nil {
		t.Fatalf("EnableRedisE() error = %v", err)
	}
	app.GET("/private", func(ctx *gin.Context) { ctx.Status(http.StatusNoContent) })
	if err := app.ApplyAll(context.Background()); err != nil {
		t.Fatalf("ApplyAll() error = %v", err)
	}
	adapter, err := ResolveE[*RedisAdapter](app.Runtime().Container)
	if err != nil || adapter == nil || adapter.Client == nil {
		t.Fatalf("configured Redis adapter = %#v, error = %v", adapter, err)
	}

	jwtUtil := NewJWTUtil(config.Auth.JWTSecret, 1)
	token, err := jwtUtil.GenerateToken(13, "configured@example.com")
	if err != nil {
		t.Fatalf("generate token: %v", err)
	}
	manager := &AuthTokenManager{JWTUtil: jwtUtil, Redis: adapter}
	if err := manager.RevokeToken(context.Background(), token); err != nil {
		t.Fatalf("revoke token: %v", err)
	}
	if response := authenticatedRequest(app, http.MethodGet, "/private", token); response.Code != http.StatusUnauthorized {
		t.Fatalf("revoked token status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}

func TestPrestartedRedisClosesWhenApplicationBuildFails(t *testing.T) {
	resetGinModeForTest(t)
	server := miniredis.RunT(t)
	config := NewSysConfig()
	config.SetFrameworkStrict(true)
	config.Redis.Required = true
	config.Redis.Addr = server.Addr()
	app, err := IgniteE(config)
	if err != nil {
		t.Fatalf("IgniteE() error = %v", err)
	}
	if err := app.EnableRedisE(context.Background()); err != nil {
		t.Fatalf("EnableRedisE() error = %v", err)
	}
	adapter, err := ResolveE[*RedisAdapter](app.Runtime().Container)
	if err != nil {
		t.Fatalf("ResolveE(RedisAdapter) error = %v", err)
	}
	if err := app.AddModuleE(&authRuntimeFailingModule{}); err != nil {
		t.Fatalf("AddModuleE() error = %v", err)
	}
	if err := app.ApplyAll(context.Background()); err == nil {
		t.Fatal("ApplyAll() error = nil, want route build failure")
	}
	if err := adapter.Client.Ping(context.Background()).Err(); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("Redis ping after rollback error = %v, want closed client", err)
	}
}

func TestShutdownClosesPrestartedRedisBeforeServe(t *testing.T) {
	resetGinModeForTest(t)
	server := miniredis.RunT(t)
	config := NewSysConfig()
	config.SetFrameworkStrict(true)
	config.Redis.Required = true
	config.Redis.Addr = server.Addr()
	app, err := IgniteE(config)
	if err != nil {
		t.Fatalf("IgniteE() error = %v", err)
	}
	if err := app.EnableRedisE(context.Background()); err != nil {
		t.Fatalf("EnableRedisE() error = %v", err)
	}
	adapter, err := ResolveE[*RedisAdapter](app.Runtime().Container)
	if err != nil {
		t.Fatalf("ResolveE(RedisAdapter) error = %v", err)
	}
	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatalf("Shutdown() error = %v", err)
	}
	if err := adapter.Client.Ping(context.Background()).Err(); err == nil || !strings.Contains(err.Error(), "closed") {
		t.Fatalf("Redis ping after Shutdown error = %v, want closed client", err)
	}
	if err := app.Shutdown(context.Background()); err != nil {
		t.Fatalf("repeated Shutdown() error = %v", err)
	}
}

func TestProductionRedisAuthRequiresUsableAdapterAtStartup(t *testing.T) {
	for _, test := range []struct {
		name    string
		adapter *RedisAdapter
	}{
		{name: "missing adapter"},
		{name: "nil client", adapter: &RedisAdapter{}},
	} {
		t.Run(test.name, func(t *testing.T) {
			resetGinModeForTest(t)
			config := NewSysConfig()
			config.Server.Mode = gin.ReleaseMode
			config.SetFrameworkStrict(true)
			config.Auth.Enabled = true
			config.Auth.StorageType = "redis"
			config.Auth.JWTSecret = randomProductionJWTKey(t)
			config.Redis.Addr = "127.0.0.1:6379"

			app, err := IgniteE(config)
			if err != nil {
				t.Fatalf("IgniteE() error = %v; startup must not dial Redis", err)
			}
			if test.adapter != nil {
				if err := app.BeansE(test.adapter); err != nil {
					t.Fatalf("BeansE(RedisAdapter) error = %v", err)
				}
			}

			err = app.ApplyAll(context.Background())
			if err == nil || !strings.Contains(strings.ToLower(err.Error()), "redis") {
				t.Fatalf("ApplyAll() error = %v, want unavailable Redis authentication storage rejection", err)
			}
		})
	}
}

func TestDevelopmentRedisAuthAlsoRequiresUsableAdapterAtStartup(t *testing.T) {
	resetGinModeForTest(t)
	config := NewSysConfig()
	config.SetFrameworkStrict(true)
	config.Auth.Enabled = true
	config.Auth.StorageType = "redis"
	config.Auth.JWTSecret = "development-redis-secret-1234567890"
	app, err := IgniteE(config)
	if err != nil {
		t.Fatalf("IgniteE() error = %v", err)
	}

	err = app.ApplyAll(context.Background())
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "redis") {
		t.Fatalf("ApplyAll() error = %v, want unavailable Redis authentication storage rejection", err)
	}
}

func TestPluginRouteAuthParticipatesInProductionWeakSecretValidation(t *testing.T) {
	resetGinModeForTest(t)
	config := NewSysConfig()
	config.Server.Mode = gin.ReleaseMode
	config.SetFrameworkStrict(true)
	config.Auth.Enabled = false
	config.Auth.JWTSecret = "bear-secret"
	app, err := IgniteE(config)
	if err != nil {
		t.Fatalf("IgniteE() error = %v", err)
	}
	module := &pluginRouteAuthModule{auth: NewAuthFairing()}
	if err := app.pluginManager.registerModule(module); err != nil {
		t.Fatalf("registerModule() error = %v", err)
	}

	err = app.ApplyAll(context.Background())
	if err == nil || !strings.Contains(err.Error(), "weak jwt secret") {
		t.Fatalf("ApplyAll() error = %v, want plugin route weak JWT secret rejection", err)
	}
}

func TestCORSMiddlewarePreflightRunsBeforeAutomaticAuth(t *testing.T) {
	resetGinModeForTest(t)
	config := NewSysConfig()
	config.Auth.Enabled = true
	config.CORS.Enabled = true
	config.CORS.AllowOrigins = []string{"https://app.example.com"}
	config.CORS.AllowMethods = []string{http.MethodGet, http.MethodOptions}

	app, err := IgniteE(config)
	if err != nil {
		t.Fatalf("IgniteE() error = %v", err)
	}
	request := httptest.NewRequest(http.MethodOptions, "/private", nil)
	request.Header.Set("Origin", "https://app.example.com")
	request.Header.Set("Access-Control-Request-Method", http.MethodGet)
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)

	if response.Code != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want %d; body=%s", response.Code, http.StatusNoContent, response.Body.String())
	}
	if got := response.Header().Get("Access-Control-Allow-Origin"); got != "https://app.example.com" {
		t.Fatalf("Access-Control-Allow-Origin = %q, want %q", got, "https://app.example.com")
	}
}

func TestCallerMiddlewareCannotBypassAutomaticAuth(t *testing.T) {
	resetGinModeForTest(t)
	config := NewSysConfig()
	config.Auth.Enabled = true
	config.Auth.JWTSecret = "automatic-auth-secret-1234567890"
	config.Auth.PublicPaths = nil

	app, err := IgniteE(config, abortBeforeAuthMiddleware(http.StatusNoContent))
	if err != nil {
		t.Fatalf("IgniteE() error = %v", err)
	}
	request := httptest.NewRequest(http.MethodGet, "/private", nil)
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("caller middleware bypass status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}

func TestManualAuthFairingProtectsGinAndCompiledRoutesWhenAutomaticAuthIsDisabled(t *testing.T) {
	for _, strict := range []bool{false, true} {
		t.Run(map[bool]string{false: "compatibility", true: "strict"}[strict], func(t *testing.T) {
			resetGinModeForTest(t)
			config := NewSysConfig()
			config.SetFrameworkStrict(strict)
			config.Auth.Enabled = false
			config.Auth.PublicPaths = nil
			app, err := IgniteE(config)
			if err != nil {
				t.Fatalf("IgniteE() error = %v", err)
			}

			jwtUtil := NewJWTUtil("manual-route-secret-1234567890", 1)
			fairing := &AuthFairing{JWTUtil: jwtUtil}
			if strict {
				if err := app.AttachE(fairing); err != nil {
					t.Fatalf("AttachE() error = %v", err)
				}
			} else {
				app.Attach(fairing)
			}

			ginHandlerReached := false
			app.GET("/gin-private", func(ctx *gin.Context) {
				ginHandlerReached = true
				ctx.Status(http.StatusNoContent)
			})
			if err := app.HandleE(http.MethodGet, "/compiled-private", func() StatusResponse {
				return StatusResponse{Status: http.StatusNoContent}
			}); err != nil {
				t.Fatalf("HandleE() error = %v", err)
			}
			if err := app.ApplyAll(context.Background()); err != nil {
				t.Fatalf("ApplyAll() error = %v", err)
			}

			for _, path := range []string{"/gin-private", "/compiled-private"} {
				response := httptest.NewRecorder()
				app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
				if response.Code != http.StatusUnauthorized {
					t.Fatalf("unauthenticated %s status = %d, want %d", path, response.Code, http.StatusUnauthorized)
				}
			}
			if ginHandlerReached {
				t.Fatal("unauthenticated Gin handler was reached")
			}

			token, err := jwtUtil.GenerateToken(17, "manual@example.com")
			if err != nil {
				t.Fatalf("GenerateToken() error = %v", err)
			}
			for _, path := range []string{"/gin-private", "/compiled-private"} {
				response := authenticatedRequest(app, http.MethodGet, path, token)
				if response.Code != http.StatusNoContent {
					t.Fatalf("authenticated %s status = %d, want %d; body=%s", path, response.Code, http.StatusNoContent, response.Body.String())
				}
			}
			if !ginHandlerReached {
				t.Fatal("authenticated Gin handler was not reached")
			}
		})
	}
}

func TestProductionManualAuthFairingValidatesEffectiveJWTStrategy(t *testing.T) {
	tests := []struct {
		name    string
		fairing *AuthFairing
		wantErr string
	}{
		{
			name:    "weak fairing JWT overrides strong config JWT",
			fairing: &AuthFairing{JWTUtil: NewJWTUtil("bear-secret", 1)},
			wantErr: "weak jwt secret",
		},
		{
			name: "weak token manager JWT",
			fairing: &AuthFairing{TokenManager: &AuthTokenManager{
				JWTUtil: NewJWTUtil("bear-secret", 1),
			}},
			wantErr: "weak jwt secret",
		},
		{
			name: "conflicting JWT strategies",
			fairing: &AuthFairing{
				JWTUtil: NewJWTUtil("manual-fairing-secret-1234567890", 1),
				TokenManager: &AuthTokenManager{
					JWTUtil: NewJWTUtil("manual-manager-secret-1234567890", 1),
				},
			},
			wantErr: "conflicting JWT",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			resetGinModeForTest(t)
			config := NewSysConfig()
			config.Server.Mode = gin.ReleaseMode
			config.SetFrameworkStrict(true)
			config.Auth.Enabled = false
			config.Auth.JWTSecret = "config-secret-1234567890"
			app, err := IgniteE(config)
			if err != nil {
				t.Fatalf("IgniteE() error = %v", err)
			}
			if err := app.AttachE(tt.fairing); err != nil {
				t.Fatalf("AttachE() error = %v", err)
			}

			err = app.ApplyAll(context.Background())
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ApplyAll() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestManualRedisAuthStorageFailsClosedWithoutUsableTokenManager(t *testing.T) {
	for _, strict := range []bool{false, true} {
		for _, tt := range []struct {
			name    string
			fairing *AuthFairing
		}{
			{
				name:    "missing token manager",
				fairing: &AuthFairing{JWTUtil: NewJWTUtil("manual-redis-secret-1234567890", 1)},
			},
			{
				name: "missing Redis adapter",
				fairing: &AuthFairing{TokenManager: &AuthTokenManager{
					JWTUtil: NewJWTUtil("manual-redis-secret-1234567890", 1),
				}},
			},
		} {
			t.Run(map[bool]string{false: "compatibility", true: "strict"}[strict]+"/"+tt.name, func(t *testing.T) {
				resetGinModeForTest(t)
				config := NewSysConfig()
				config.SetFrameworkStrict(strict)
				config.Auth.Enabled = false
				config.Auth.StorageType = "redis"
				app, err := IgniteE(config)
				if err != nil {
					t.Fatalf("IgniteE() error = %v", err)
				}
				if strict {
					if err := app.AttachE(tt.fairing); err != nil {
						t.Fatalf("AttachE() error = %v", err)
					}
				} else {
					app.Attach(tt.fairing)
				}

				err = app.ApplyAll(context.Background())
				if err == nil || !strings.Contains(err.Error(), "auth.storage_type=redis") {
					t.Fatalf("ApplyAll() error = %v, want Redis authentication storage rejection", err)
				}
				if _, err := ResolveE[*RedisAdapter](app.Runtime().Container); !errors.Is(err, ErrBeanMissing) {
					t.Fatalf("RedisAdapter resolution after failed manual auth startup = %v, want ErrBeanMissing", err)
				}
			})
		}
	}
}

func TestCompatibilityModuleRedisAuthFailsClosedAfterRouteBuild(t *testing.T) {
	resetGinModeForTest(t)
	config := NewSysConfig()
	config.Auth.Enabled = false
	config.Auth.StorageType = "redis"
	app, err := IgniteE(config)
	if err != nil {
		t.Fatalf("IgniteE() error = %v", err)
	}
	app.AddModule(&authRuntimeCompatibilityRedisModule{
		auth: &AuthFairing{JWTUtil: NewJWTUtil("compatibility-redis-secret-1234567890", 1)},
	})

	err = app.ApplyAll(context.Background())
	if err == nil || !strings.Contains(err.Error(), "auth.storage_type=redis") {
		t.Fatalf("ApplyAll() error = %v, want route-built Redis authentication storage rejection", err)
	}
}

func TestNativeGinControllerAuthParticipatesInProductionValidation(t *testing.T) {
	for _, tt := range []struct {
		name        string
		storageType string
		jwtSecret   string
		wantErr     string
	}{
		{name: "weak JWT", storageType: "jwt", jwtSecret: "bear-secret", wantErr: "weak jwt secret"},
		{name: "missing Redis revocation", storageType: "redis", jwtSecret: "native-controller-secret-1234567890", wantErr: "auth.storage_type=redis"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			resetGinModeForTest(t)
			config := NewSysConfig()
			config.Server.Mode = gin.ReleaseMode
			config.SetFrameworkStrict(true)
			config.Auth.Enabled = false
			config.Auth.StorageType = tt.storageType
			config.Redis.Addr = "127.0.0.1:6379"
			app := Ignite(config)
			controller := &nativeControllerAuth{auth: &AuthFairing{JWTUtil: NewJWTUtil(tt.jwtSecret, 1)}}
			if err := app.AddModuleE(&nativeControllerAuthModule{controller: controller}); err != nil {
				t.Fatalf("AddModuleE() error = %v", err)
			}

			err := app.ApplyAll(context.Background())
			if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
				t.Fatalf("ApplyAll() error = %v, want %q", err, tt.wantErr)
			}
		})
	}
}

func TestExplicitTokenManagerWithoutJWTUsesFairingJWT(t *testing.T) {
	resetGinModeForTest(t)
	config := NewSysConfig()
	config.SetFrameworkStrict(true)
	config.Auth.Enabled = false
	config.Auth.PublicPaths = nil
	jwtUtil := NewJWTUtil("explicit-manager-fallback-secret-1234567890", 1)
	manager := NewAuthTokenManager()
	app, err := IgniteE(config)
	if err != nil {
		t.Fatalf("IgniteE() error = %v", err)
	}
	if err := app.AttachE(&AuthFairing{JWTUtil: jwtUtil, TokenManager: manager}); err != nil {
		t.Fatalf("AttachE() error = %v", err)
	}
	app.GET("/private", func(ctx *gin.Context) { ctx.Status(http.StatusNoContent) })
	if err := app.ApplyAll(context.Background()); err != nil {
		t.Fatalf("ApplyAll() error = %v", err)
	}
	if manager.JWTUtil != jwtUtil {
		t.Fatalf("TokenManager JWTUtil = %p, want fairing JWTUtil %p", manager.JWTUtil, jwtUtil)
	}
	token, err := jwtUtil.GenerateToken(23, "manager-fallback@example.com")
	if err != nil {
		t.Fatalf("GenerateToken() error = %v", err)
	}
	if response := authenticatedRequest(app, http.MethodGet, "/private", token); response.Code != http.StatusNoContent {
		t.Fatalf("authenticated status = %d, want %d; body=%s", response.Code, http.StatusNoContent, response.Body.String())
	}
}

func TestManualRedisAuthCanOpenConfiguredRevocationStorage(t *testing.T) {
	resetGinModeForTest(t)
	server := miniredis.RunT(t)
	config := NewSysConfig()
	config.SetFrameworkStrict(true)
	config.Auth.Enabled = false
	config.Auth.StorageType = "redis"
	config.Auth.PublicPaths = nil
	config.Redis.Addr = server.Addr()
	app, err := IgniteE(config)
	if err != nil {
		t.Fatalf("IgniteE() error = %v", err)
	}
	if err := app.EnableRedisE(context.Background()); err != nil {
		t.Fatalf("EnableRedisE() error = %v", err)
	}

	jwtUtil := NewJWTUtil("manual-redis-open-secret-1234567890", 1)
	auth := &AuthFairing{JWTUtil: jwtUtil}
	if err := app.AttachE(auth); err != nil {
		t.Fatalf("AttachE() error = %v", err)
	}
	app.GET("/private", func(ctx *gin.Context) { ctx.Status(http.StatusNoContent) })
	if err := app.ApplyAll(context.Background()); err != nil {
		t.Fatalf("ApplyAll() error = %v", err)
	}
	if auth.TokenManager == nil || auth.TokenManager.Redis == nil || auth.TokenManager.Redis.Client == nil {
		t.Fatalf("manual Redis auth policy = %#v, want usable revocation manager", auth)
	}

	token, err := jwtUtil.GenerateToken(23, "manual-redis@example.com")
	if err != nil {
		t.Fatalf("GenerateToken() error = %v", err)
	}
	if err := auth.TokenManager.RevokeToken(context.Background(), token); err != nil {
		t.Fatalf("RevokeToken() error = %v", err)
	}
	if response := authenticatedRequest(app, http.MethodGet, "/private", token); response.Code != http.StatusUnauthorized {
		t.Fatalf("revoked manual token status = %d, want %d", response.Code, http.StatusUnauthorized)
	}
}

func authenticatedRequest(handler http.Handler, method, path, token string) *httptest.ResponseRecorder {
	request := httptest.NewRequest(method, path, nil)
	request.Header.Set("Authorization", "Bearer "+token)
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, request)
	return response
}
