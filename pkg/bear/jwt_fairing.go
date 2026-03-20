package bear

import (
	"strings"

	"github.com/gin-gonic/gin"
)

// AuthFairing JWT 鉴权拦截器
type AuthFairing struct {
	BaseFairing
	JWTUtil     *JWTUtil          `inject:"-"`
	TokenManager *AuthTokenManager `inject:"-"`
}

func NewAuthFairing() *AuthFairing {
	return &AuthFairing{}
}

func (this *AuthFairing) OnRequest(ctx *gin.Context) error {
	// 演示：如果是 /v1/hello 或 /login 或 /cache 则跳过
	path := ctx.Request.URL.Path
	if strings.Contains(path, "/hello") || strings.Contains(path, "/login") || strings.Contains(path, "/cache") || strings.Contains(path, "/async-task") || strings.Contains(path, "/status") ||
		path == "/health" || path == "/ready" || path == "/live" || path == "/metrics" || strings.Contains(path, "/error-demo") || strings.HasPrefix(path, "/swagger") {
		return nil
	}

	authHeader := ctx.GetHeader("Authorization")
	if authHeader == "" {
		return NewError(401, "authorization header is required")
	}

	parts := strings.SplitN(authHeader, " ", 2)
	if !(len(parts) == 2 && parts[0] == "Bearer") {
		return NewError(401, "authorization header format must be Bearer {token}")
	}

	tokenStr := parts[1]

	// Use TokenManager if available, otherwise fallback to JWTUtil
	var claims *CustomClaims
	var err error

	if this.TokenManager != nil {
		claims, err = this.TokenManager.ParseToken(tokenStr)
	} else {
		claims, err = this.JWTUtil.ParseToken(tokenStr)
	}

	if err != nil {
		return NewError(401, "invalid or expired token")
	}

	// 将用户信息存入上下文
	ctx.Set("current_user_id", claims.UserID)
	ctx.Set("current_token", tokenStr) // Save token for logout
	return nil
}

func (this *AuthFairing) Name() string {
	return "AuthFairing"
}
