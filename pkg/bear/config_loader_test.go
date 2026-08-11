package bear

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/golang-jwt/jwt/v5"
)

func TestLoadConfigRejectsUnknownProductionKey(t *testing.T) {
	path := writeConfig(t, "application.yaml", "server:\n  port: 8080\n  name: app\n  typo_timeout: 3s\n")
	t.Setenv("BEAR_ENV", "prod")

	_, err := LoadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "typo_timeout") {
		t.Fatalf("expected unknown-field error, got %v", err)
	}
}

func TestLoadConfigStrictPolicyByEnvironment(t *testing.T) {
	t.Run("development defaults to strict", func(t *testing.T) {
		t.Setenv("BEAR_ENV", "dev")
		path := writeConfig(t, "application.yaml", "server:\n  typo_timeout: 3s\n")
		_, err := LoadConfig(path)
		if err == nil || !strings.Contains(err.Error(), "typo_timeout") {
			t.Fatalf("expected unknown-field error, got %v", err)
		}
	})

	t.Run("development may disable strict loading", func(t *testing.T) {
		t.Setenv("BEAR_ENV", "dev")
		path := writeConfig(t, "application.yaml", "config:\n  strict: false\nserver:\n  name: compatible\n  legacy_extension: true\n")
		cfg, err := LoadConfig(path)
		if err != nil {
			t.Fatalf("LoadConfig failed: %v", err)
		}
		if cfg.Server.Name != "compatible" {
			t.Fatalf("server name = %q", cfg.Server.Name)
		}
	})

	t.Run("production cannot disable strict loading", func(t *testing.T) {
		t.Setenv("BEAR_ENV", "production")
		path := writeConfig(t, "application.yaml", "config:\n  strict: false\n")
		_, err := LoadConfig(path)
		if err == nil || !strings.Contains(err.Error(), "config.strict") {
			t.Fatalf("expected production strict-policy error, got %v", err)
		}
	})

	t.Run("release mode cannot disable strict loading", func(t *testing.T) {
		t.Setenv("BEAR_ENV", "dev")
		path := writeConfig(t, "application.yaml", "config:\n  strict: false\nserver:\n  mode: release\n")
		_, err := LoadConfig(path)
		if err == nil || !strings.Contains(err.Error(), "config.strict") {
			t.Fatalf("expected release-mode strict-policy error, got %v", err)
		}
	})

	t.Run("GIN release mode cannot disable strict loading", func(t *testing.T) {
		t.Setenv("BEAR_ENV", "dev")
		t.Setenv("GIN_MODE", "release")
		path := writeConfig(t, "application.yaml", "config:\n  strict: false\n")
		_, err := LoadConfig(path)
		if err == nil || !strings.Contains(err.Error(), "config.strict") {
			t.Fatalf("expected GIN release strict-policy error, got %v", err)
		}
	})
}

func TestLoadConfigPreservesProductionEnvironmentFilename(t *testing.T) {
	t.Setenv("BEAR_ENV", "production")
	t.Setenv("JWT_SECRET", randomProductionJWTKey(t))
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(workingDirectory); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "application-production.yaml"), []byte("server:\n  name: production-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(directory); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	if cfg.Server.Name != "production-file" {
		t.Fatalf("server name = %q", cfg.Server.Name)
	}
}

func TestUppercaseProductionEnvironmentUsesRawOverlayAndProductionSafeguards(t *testing.T) {
	t.Setenv("BEAR_ENV", "PRODUCTION")
	t.Setenv("GIN_MODE", "")
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(workingDirectory); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "application-PRODUCTION.yaml"), []byte("server:\n  name: uppercase-production-file\nconfig:\n  strict: false\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(directory); err != nil {
		t.Fatal(err)
	}

	if _, err := LoadConfig(); err == nil || !strings.Contains(err.Error(), "config.strict") {
		t.Fatalf("expected uppercase production strict-policy error, got %v", err)
	}

	if got := configuredGinMode(NewSysConfig()); got != gin.ReleaseMode {
		t.Fatalf("gin mode = %q, want %q", got, gin.ReleaseMode)
	}
	if !isProductionMode(NewSysConfig()) {
		t.Fatal("uppercase production environment did not activate production safeguards")
	}
	weak := NewSysConfig()
	weak.Auth.JWTSecret = "bear-secret"
	if err := validateProductionSecurity(weak); err == nil || !strings.Contains(err.Error(), "weak jwt secret") {
		t.Fatalf("expected uppercase production secret validation error, got %v", err)
	}

	if err := os.WriteFile(filepath.Join(directory, "application-PRODUCTION.yaml"), []byte("server:\n  name: uppercase-production-file\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("JWT_SECRET", randomProductionJWTKey(t))
	cfg, err := LoadConfig()
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	if cfg.Server.Name != "uppercase-production-file" {
		t.Fatalf("server name = %q", cfg.Server.Name)
	}
}

func TestStrictPolicyHandlesNilCurrentConfig(t *testing.T) {
	strict, err := strictPolicy([]byte("server:\n  name: app\n"), "application.yaml", nil, false)
	if err != nil {
		t.Fatalf("strictPolicy failed: %v", err)
	}
	if !strict {
		t.Fatal("development must remain strict by default")
	}
}

func TestLoadConfigRejectsUnknownJSONFieldInStrictMode(t *testing.T) {
	t.Setenv("BEAR_ENV", "prod")
	path := writeConfig(t, "config.json", `{"server":{"name":"app","unknown_limit":42}}`)

	_, err := LoadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "unknown_limit") {
		t.Fatalf("expected JSON unknown-field error, got %v", err)
	}
}

func TestLoadConfigReturnsSyntaxAndMissingFileErrors(t *testing.T) {
	t.Setenv("BEAR_ENV", "dev")
	t.Run("syntax", func(t *testing.T) {
		path := writeConfig(t, "application.yaml", "server: [")
		_, err := LoadConfig(path)
		if err == nil || !strings.Contains(err.Error(), "failed to parse YAML") {
			t.Fatalf("expected syntax error, got %v", err)
		}
	})

	t.Run("missing explicit path", func(t *testing.T) {
		_, err := LoadConfig(filepath.Join(t.TempDir(), "missing.yaml"))
		if !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("expected not-exist error, got %v", err)
		}
	})
}

func TestLoadConfigMergesExplicitPathsAndAppliesEnvironment(t *testing.T) {
	t.Setenv("BEAR_ENV", "dev")
	t.Setenv("BEAR_SERVER_PORT", "9191")
	base := writeConfig(t, "base.yaml", "server:\n  name: base\n  read_timeout: 10s\n")
	overlay := writeConfig(t, "overlay.json", `{"server":{"name":"overlay"}}`)

	cfg, err := LoadConfig(base, overlay)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	if cfg.Server.Name != "overlay" || cfg.Server.ReadTimeout != "10s" || cfg.Server.Port != 9191 {
		t.Fatalf("unexpected merged config: %#v", cfg.Server)
	}
}

func TestFrameworkRuntimeContractDefaults(t *testing.T) {
	cfg := NewSysConfig()
	if cfg.FrameworkStrict() || cfg.ResponseMode() != "raw" {
		t.Fatalf("defaults = strict:%v mode:%q", cfg.FrameworkStrict(), cfg.ResponseMode())
	}
}

func TestFrameworkRuntimeContractReadsExtensionKeys(t *testing.T) {
	t.Setenv("BEAR_ENV", "dev")
	path := writeConfig(t, "application.yaml", "config:\n  framework.strict: true\n  framework.response_mode: envelope\n")

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	if !cfg.FrameworkStrict() || cfg.ResponseMode() != "envelope" {
		t.Fatalf("contract = strict:%v mode:%q", cfg.FrameworkStrict(), cfg.ResponseMode())
	}
}

func TestSetResponseModeRejectsUnknownMode(t *testing.T) {
	cfg := NewSysConfig()
	if err := cfg.SetResponseMode("unknown"); err == nil {
		t.Fatal("SetResponseMode accepted an unknown response mode")
	}
	if cfg.ResponseMode() != "raw" {
		t.Fatalf("response mode = %q, want raw", cfg.ResponseMode())
	}
}

func TestFrameworkRuntimeContractValidateRejectsInvalidResponseModeExtensionValues(t *testing.T) {
	tests := []struct {
		name  string
		value any
	}{
		{name: "unknown string", value: "unknown"},
		{name: "non-string", value: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cfg := NewSysConfig()
			cfg.Config["framework.response_mode"] = tt.value

			err := cfg.Validate()
			if err == nil || !strings.Contains(err.Error(), "framework.response_mode") {
				t.Fatalf("Validate error = %v, want framework.response_mode rejection", err)
			}
		})
	}
}

func TestInitConfigPreservesPanicOnErrorCompatibility(t *testing.T) {
	t.Setenv("BEAR_ENV", "dev")
	t.Setenv("GIN_MODE", "")
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(workingDirectory); err != nil {
			t.Errorf("restore working directory: %v", err)
		}
	})
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "application.yaml"), []byte("server: ["), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(directory); err != nil {
		t.Fatal(err)
	}

	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("expected InitConfig to panic on a configuration error")
		}
	}()
	InitConfig()
}

func TestDefaultProxyPolicyIgnoresSpoofedForwardedFor(t *testing.T) {
	app := Ignite(NewSysConfig())
	request := httptest.NewRequest(http.MethodGet, "/ip", nil)
	request.Header.Set("X-Forwarded-For", "203.0.113.9")

	assertClientIP(t, app, request, request.RemoteAddr)
}

func TestProxyExplicitPolicyHonorsForwardedFor(t *testing.T) {
	cfg := NewSysConfig()
	cfg.Server.TrustedProxies = []string{"127.0.0.1"}
	app := Ignite(cfg)
	request := httptest.NewRequest(http.MethodGet, "/ip", nil)
	request.RemoteAddr = "127.0.0.1:12345"
	request.Header.Set("X-Forwarded-For", "203.0.113.9")

	assertClientIP(t, app, request, "203.0.113.9")
}

func TestRequestBodyLimitReturns413(t *testing.T) {
	cfg := NewSysConfig()
	cfg.Server.MaxRequestBodyBytes = 32

	assertLargeJSONStatus(t, Ignite(cfg), http.StatusRequestEntityTooLarge)
}

func TestRequestBodyDefaultLimitReturns413WhenConfigIsZero(t *testing.T) {
	cfg := NewSysConfig()
	cfg.Server.MaxRequestBodyBytes = 0
	app := Ignite(cfg)
	type requestBody struct {
		Value string `json:"value"`
	}
	app.Handle(http.MethodPost, "/body", func(request *requestBody) string { return request.Value })
	body := []byte(`{"value":"` + strings.Repeat("x", (1<<20)+1) + `"}`)
	request := httptest.NewRequest(http.MethodPost, "/body", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	app.ServeHTTP(response, request)

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413", response.Code)
	}
}

func TestRequestBodyLimitReturns413WithoutContentLength(t *testing.T) {
	cfg := NewSysConfig()
	cfg.Server.MaxRequestBodyBytes = 32
	app := Ignite(cfg)
	type requestBody struct {
		Value string `json:"value"`
	}
	app.Handle(http.MethodPost, "/body", func(request *requestBody) string { return request.Value })
	body := []byte(`{"value":"` + strings.Repeat("x", 64) + `"}`)
	request := httptest.NewRequest(http.MethodPost, "/body", bytes.NewReader(body))
	request.ContentLength = -1
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()

	app.ServeHTTP(response, request)

	if response.Code != http.StatusRequestEntityTooLarge {
		t.Fatalf("status = %d, want 413; body = %s", response.Code, response.Body.String())
	}
}

func TestRequestBodyAndHeaderDefaultsAreOneMiB(t *testing.T) {
	cfg := NewSysConfig()
	if cfg.Server.MaxHeaderBytes != 1<<20 || cfg.Server.MaxRequestBodyBytes != 1<<20 {
		t.Fatalf("defaults = header:%d body:%d", cfg.Server.MaxHeaderBytes, cfg.Server.MaxRequestBodyBytes)
	}
	cfg.Server.MaxHeaderBytes = 0
	server := Ignite(cfg).buildHTTPServer(cfg)
	if server.MaxHeaderBytes != 1<<20 {
		t.Fatalf("HTTP server MaxHeaderBytes = %d, want %d", server.MaxHeaderBytes, 1<<20)
	}
}

func TestRequestBodyAndHeaderLimitsRejectNegativeConfiguration(t *testing.T) {
	for _, contents := range []string{
		"server:\n  max_header_bytes: -1\n",
		"server:\n  max_request_body_bytes: -1\n",
	} {
		path := writeConfig(t, "application.yaml", contents)
		_, err := LoadConfig(path)
		if err == nil || !strings.Contains(err.Error(), "must not be negative") {
			t.Fatalf("expected negative-limit error, got %v", err)
		}
	}
}

func TestRequestIDRejectsInvalidClientValues(t *testing.T) {
	for _, requestID := range []string{
		"contains spaces",
		"line\nbreak",
		strings.Repeat("a", 129),
	} {
		t.Run(requestID[:min(len(requestID), 16)], func(t *testing.T) {
			app := Ignite(NewSysConfig())
			app.GET("/id", func(ctx *gin.Context) { ctx.Status(http.StatusNoContent) })
			request := httptest.NewRequest(http.MethodGet, "/id", nil)
			request.Header.Set("X-Request-ID", requestID)
			response := httptest.NewRecorder()

			app.ServeHTTP(response, request)

			got := response.Header().Get("X-Request-ID")
			if got == requestID || got == "" {
				t.Fatalf("request id = %q, want generated value", got)
			}
		})
	}
}

func TestRequestIDPreservesValidClientValue(t *testing.T) {
	app := Ignite(NewSysConfig())
	app.GET("/id", func(ctx *gin.Context) { ctx.Status(http.StatusNoContent) })
	request := httptest.NewRequest(http.MethodGet, "/id", nil)
	request.Header.Set("X-Request-ID", "client_request-123.valid")
	response := httptest.NewRecorder()

	app.ServeHTTP(response, request)

	if got := response.Header().Get("X-Request-ID"); got != "client_request-123.valid" {
		t.Fatalf("request id = %q", got)
	}
}

func TestSecurityHeadersUseConservativeDefaultsWithoutHSTS(t *testing.T) {
	app := Ignite(NewSysConfig())
	app.GET("/headers", func(ctx *gin.Context) { ctx.Status(http.StatusNoContent) })
	response := httptest.NewRecorder()

	app.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/headers", nil))

	for name, want := range map[string]string{
		"X-Content-Type-Options": "nosniff",
		"X-Frame-Options":        "DENY",
		"Referrer-Policy":        "no-referrer",
	} {
		if got := response.Header().Get(name); got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
	if got := response.Header().Get("Strict-Transport-Security"); got != "" {
		t.Fatalf("unexpected HSTS header %q", got)
	}
}

func TestCORSRejectsWildcardOriginWithCredentials(t *testing.T) {
	cfg := NewSysConfig()
	cfg.CORS.Enabled = true
	cfg.CORS.AllowOrigins = []string{"https://example.com", "*"}
	cfg.CORS.AllowCredentials = true

	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "wildcard") {
		t.Fatalf("expected CORS wildcard validation error, got %v", err)
	}
	defer func() {
		if recovered := recover(); recovered == nil {
			t.Fatal("expected Ignite to reject invalid CORS policy")
		}
	}()
	Ignite(cfg)
}

func TestLoadConfigParsesJWTValidationPolicy(t *testing.T) {
	t.Setenv("BEAR_ENV", "dev")
	path := writeConfig(t, "application.yaml", "auth:\n  jwt_issuer: https://issuer.example\n  jwt_audience: bear-api\n  jwt_clock_skew: 45s\n")

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}
	if cfg.Auth.JWTIssuer != "https://issuer.example" || cfg.Auth.JWTAudience != "bear-api" || cfg.Auth.JWTClockSkew != "45s" {
		t.Fatalf("unexpected JWT policy: %#v", cfg.Auth)
	}
}

func TestLoadConfigRejectsJWTClockSkewOverFiveMinutes(t *testing.T) {
	t.Setenv("BEAR_ENV", "dev")
	for _, skew := range []string{"5m1s", "-1s", "not-a-duration"} {
		t.Run(skew, func(t *testing.T) {
			path := writeConfig(t, "application.yaml", "auth:\n  jwt_clock_skew: "+skew+"\n")
			_, err := LoadConfig(path)
			if err == nil || !strings.Contains(err.Error(), "auth.jwt_clock_skew") {
				t.Fatalf("expected clock-skew validation error, got %v", err)
			}
		})
	}
}

func TestJWTValidatesConfiguredIssuerAndAudience(t *testing.T) {
	util := NewJWTUtil("secret-1234567890", 1)
	util.Config.Issuer = "https://issuer.example"
	util.Config.Audience = "bear-api"

	token, err := util.GenerateToken(7, "bear@example.com")
	if err != nil {
		t.Fatalf("GenerateToken failed: %v", err)
	}
	claims, err := util.ParseToken(token)
	if err != nil {
		t.Fatalf("ParseToken failed: %v", err)
	}
	if claims.Issuer != util.Config.Issuer || !slices.Contains(claims.Audience, util.Config.Audience) {
		t.Fatalf("registered claims = %#v", claims.RegisteredClaims)
	}

	wrongIssuer := signedJWT(t, util.Config.Secret, jwt.RegisteredClaims{
		Issuer:    "https://other.example",
		Audience:  jwt.ClaimStrings{util.Config.Audience},
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
	})
	if _, err := util.ParseToken(wrongIssuer); err == nil {
		t.Fatal("expected issuer validation failure")
	}
	wrongAudience := signedJWT(t, util.Config.Secret, jwt.RegisteredClaims{
		Issuer:    util.Config.Issuer,
		Audience:  jwt.ClaimStrings{"other-api"},
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
	})
	if _, err := util.ParseToken(wrongAudience); err == nil {
		t.Fatal("expected audience validation failure")
	}
}

func TestIgniteConfiguresAuthFairingJWTValidationFromSysConfig(t *testing.T) {
	resetTestInjector()
	resetGinModeForTest(t)
	cfg := NewSysConfig()
	cfg.DB.Enabled = false
	cfg.Auth.JWTSecret = "secret-1234567890"
	cfg.Auth.JWTIssuer = "https://issuer.example"
	cfg.Auth.JWTAudience = "bear-api"
	cfg.Auth.JWTClockSkew = "1m"
	cfg.Auth.PublicPaths = nil

	app := Ignite(cfg)
	app.Attach(NewAuthFairing())
	app.Handle(http.MethodGet, "/private", func() string { return "ok" })
	if err := app.ApplyAll(context.Background()); err != nil {
		t.Fatalf("ApplyAll failed: %v", err)
	}

	tests := []struct {
		name       string
		claims     jwt.RegisteredClaims
		wantStatus int
	}{
		{
			name: "configured claims are accepted",
			claims: jwt.RegisteredClaims{
				Issuer:    cfg.Auth.JWTIssuer,
				Audience:  jwt.ClaimStrings{cfg.Auth.JWTAudience},
				ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			},
			wantStatus: http.StatusOK,
		},
		{
			name: "wrong issuer is rejected",
			claims: jwt.RegisteredClaims{
				Issuer:    "https://other.example",
				Audience:  jwt.ClaimStrings{cfg.Auth.JWTAudience},
				ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "wrong audience is rejected",
			claims: jwt.RegisteredClaims{
				Issuer:    cfg.Auth.JWTIssuer,
				Audience:  jwt.ClaimStrings{"other-api"},
				ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Hour)),
			},
			wantStatus: http.StatusUnauthorized,
		},
		{
			name: "configured clock skew is accepted",
			claims: jwt.RegisteredClaims{
				Issuer:    cfg.Auth.JWTIssuer,
				Audience:  jwt.ClaimStrings{cfg.Auth.JWTAudience},
				ExpiresAt: jwt.NewNumericDate(time.Now().Add(-30 * time.Second)),
			},
			wantStatus: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			token := signedJWT(t, cfg.Auth.JWTSecret, tt.claims)
			request := httptest.NewRequest(http.MethodGet, "/private", nil)
			request.Header.Set("Authorization", "Bearer "+token)
			response := httptest.NewRecorder()

			app.ServeHTTP(response, request)

			if response.Code != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body = %s", response.Code, tt.wantStatus, response.Body.String())
			}
		})
	}
}

func TestJWTClockSkewIsAppliedAndBounded(t *testing.T) {
	util := NewJWTUtil("secret-1234567890", 1)
	util.Config.ClockSkew = time.Minute
	token := signedJWT(t, util.Config.Secret, jwt.RegisteredClaims{
		ExpiresAt: jwt.NewNumericDate(time.Now().Add(-30 * time.Second)),
	})
	if _, err := util.ParseToken(token); err != nil {
		t.Fatalf("ParseToken with allowed skew failed: %v", err)
	}

	util.Config.ClockSkew = 5*time.Minute + time.Nanosecond
	if _, err := util.ParseToken(token); err == nil || !strings.Contains(err.Error(), "clock skew") {
		t.Fatalf("expected bounded clock-skew error, got %v", err)
	}
}

func TestAuthTokenValidationSucceedsWithoutRedis(t *testing.T) {
	util := NewJWTUtil("secret-1234567890", 1)
	token, err := util.GenerateToken(9, "bear@example.com")
	if err != nil {
		t.Fatal(err)
	}
	manager := &AuthTokenManager{JWTUtil: util}

	claims, err := manager.ParseToken(token)
	if err != nil {
		t.Fatalf("ParseToken failed without Redis: %v", err)
	}
	if claims.UserID != 9 {
		t.Fatalf("user id = %d", claims.UserID)
	}
}

func TestAuthTokenRevokeReturnsTypedUnavailableErrorWithoutRedis(t *testing.T) {
	util := NewJWTUtil("secret-1234567890", 1)
	token, err := util.GenerateToken(9, "bear@example.com")
	if err != nil {
		t.Fatal(err)
	}

	for _, manager := range []*AuthTokenManager{
		{JWTUtil: util},
		{JWTUtil: util, Redis: &RedisAdapter{}},
	} {
		err := manager.RevokeToken(context.Background(), token)
		if !errors.Is(err, ErrTokenRevocationUnavailable) {
			t.Fatalf("RevokeToken error = %v", err)
		}
	}
}

func TestJWTClockSkewValidationRunsAtIgnite(t *testing.T) {
	cfg := NewSysConfig()
	cfg.Auth.JWTClockSkew = "6m"

	defer func() {
		recovered := recover()
		message, ok := recovered.(string)
		if !ok || !strings.Contains(message, "auth.jwt_clock_skew") {
			t.Fatalf("unexpected panic = %v", recovered)
		}
	}()
	Ignite(cfg)
}

func TestLoadConfigRunsProductionSecretValidation(t *testing.T) {
	t.Setenv("BEAR_ENV", "prod")
	path := writeConfig(t, "application.yaml", "auth:\n  jwt_secret: bear-secret\n")

	_, err := LoadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "weak jwt secret") {
		t.Fatalf("expected production secret error, got %v", err)
	}
}

func TestLoadConfigRejectsWhitespaceProductionSecret(t *testing.T) {
	t.Setenv("BEAR_ENV", "prod")
	path := writeConfig(t, "application.yaml", "auth:\n  jwt_secret: '                                '\n")

	_, err := LoadConfig(path)
	if err == nil || !strings.Contains(err.Error(), "weak jwt secret") {
		t.Fatalf("expected production secret error, got %v", err)
	}
}

func TestSysConfigValidateRejectsKnownProductionJWTSecrets(t *testing.T) {
	t.Setenv("BEAR_ENV", "production")
	t.Setenv("GIN_MODE", "")

	for _, placeholder := range []string{
		"bear-secret",
		"your-secret-key",
		"set-with-JWT_SECRET-32-plus-random-chars",
		"replace-with-at-least-32-random-characters",
		"test-only-jwt-key-with-32-random-characters",
		"release-e2e-jwt-secret-1234567890",
		"test-production-secret-with-32-characters",
		"production-secret-with-at-least-32-characters",
		"env-secret-with-at-least-32-characters",
	} {
		placeholder := placeholder
		t.Run(placeholder, func(t *testing.T) {
			cfg := NewSysConfig()
			cfg.Auth.JWTSecret = placeholder

			if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "weak jwt secret") {
				t.Fatalf("SysConfig.Validate accepted known JWT placeholder %q: %v", placeholder, err)
			}
		})
	}
}

func TestSysConfigValidateAccepts32ByteJWTSecretWithBoundaryWhitespace(t *testing.T) {
	t.Setenv("BEAR_ENV", "production")
	t.Setenv("GIN_MODE", "")
	secretBytes := make([]byte, 32)
	secretBytes[0] = ' '
	secretBytes[len(secretBytes)-1] = ' '
	if _, err := rand.Read(secretBytes[1 : len(secretBytes)-1]); err != nil {
		t.Fatalf("generate random JWT secret content: %v", err)
	}
	secret := string(secretBytes)
	if len(secret) != 32 {
		t.Fatalf("test JWT secret length = %d", len(secret))
	}

	cfg := NewSysConfig()
	cfg.Auth.JWTSecret = secret
	if err := cfg.Validate(); err != nil {
		t.Fatalf("SysConfig.Validate rejected 32-byte JWT secret with boundary whitespace: %v", err)
	}
}

func TestDefaultAuthPathsDoNotExposeMetrics(t *testing.T) {
	if slices.Contains(NewSysConfig().Auth.GetPublicPaths(), "/metrics") {
		t.Fatal("default auth public paths expose /metrics")
	}
}

func TestProductionExampleUsesHardenedDefaults(t *testing.T) {
	path := filepath.Join("..", "..", "application-prod.yaml.example")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	cfg := NewSysConfig()
	if err := decodeConfig(data, path, cfg, true); err != nil {
		t.Fatalf("decode production example: %v", err)
	}
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "weak jwt secret") {
		t.Fatalf("production example placeholder was not rejected by Validate: %v", err)
	}
	if cfg.DB.SSLMode != "verify-full" {
		t.Fatalf("database.sslmode = %q", cfg.DB.SSLMode)
	}
	if cfg.DB.Password != "" {
		t.Fatal("production example must leave database password empty for POSTGRES_PASSWORD")
	}
	if slices.Contains(cfg.Auth.GetPublicPaths(), "/metrics") {
		t.Fatal("production example exposes /metrics as an auth public path")
	}
	if cfg.Auth.JWTSecret != "" {
		t.Fatalf("production example JWT placeholder = %q, want empty", cfg.Auth.JWTSecret)
	}

	if err := validateProductionSecurity(cfg); err == nil || !strings.Contains(err.Error(), "weak jwt secret") {
		t.Fatalf("production placeholder was not rejected by final guard: %v", err)
	}
	t.Setenv("BEAR_ENV", "production")
	t.Setenv("BEAR_AUTH_JWT_SECRET", "")
	t.Setenv("JWT_SECRET", "")
	if err := cfg.Validate(); err == nil || !strings.Contains(err.Error(), "weak jwt secret") {
		t.Fatalf("SysConfig.Validate accepted production example without BEAR_AUTH_JWT_SECRET: %v", err)
	}
	if _, err := LoadConfig(path); err == nil || !strings.Contains(err.Error(), "weak jwt secret") {
		t.Fatalf("production example without BEAR_AUTH_JWT_SECRET was not rejected: %v", err)
	}

	t.Setenv("BEAR_AUTH_JWT_SECRET", randomProductionJWTKey(t))
	loaded, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("production example with strong BEAR_AUTH_JWT_SECRET failed: %v", err)
	}
	if err := loaded.Validate(); err != nil {
		t.Fatalf("validate production example with injected secret: %v", err)
	}
	if err := validateProductionSecurity(loaded); err != nil {
		t.Fatalf("production startup rejected injected secret: %v", err)
	}
}

func signedJWT(t *testing.T, secret string, claims jwt.RegisteredClaims) string {
	t.Helper()
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, CustomClaims{RegisteredClaims: claims})
	signed, err := token.SignedString([]byte(secret))
	if err != nil {
		t.Fatal(err)
	}
	return signed
}

func writeConfig(t *testing.T, name, contents string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func assertClientIP(t *testing.T, app *Bear, request *http.Request, wantRemoteAddr string) {
	t.Helper()
	app.GET("/ip", func(ctx *gin.Context) {
		ctx.String(http.StatusOK, ctx.ClientIP())
	})
	want, _, err := net.SplitHostPort(wantRemoteAddr)
	if err != nil {
		want = wantRemoteAddr
	}
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)
	if got := response.Body.String(); got != want {
		t.Fatalf("client ip = %q, want %q", got, want)
	}
}

func assertLargeJSONStatus(t *testing.T, app *Bear, want int) {
	t.Helper()
	type requestBody struct {
		Value string `json:"value"`
	}
	app.Handle(http.MethodPost, "/body", func(request *requestBody) string {
		return request.Value
	})
	body := []byte(`{"value":"` + strings.Repeat("x", 64) + `"}`)
	request := httptest.NewRequest(http.MethodPost, "/body", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	response := httptest.NewRecorder()
	app.ServeHTTP(response, request)
	if response.Code != want {
		t.Fatalf("status = %d, want %d; body = %s", response.Code, want, response.Body.String())
	}
}
