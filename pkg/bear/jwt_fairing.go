package bear

import (
	"strings"

	"github.com/gin-gonic/gin"
)

// AuthFairing JWT 鉴权拦截器
type AuthFairing struct {
	BaseFairing
	JWTUtil      *JWTUtil          `inject:"-"`
	TokenManager *AuthTokenManager `inject:"-"`
}

func NewAuthFairing() *AuthFairing {
	return &AuthFairing{}
}

func (f *AuthFairing) OnRequest(ctx *gin.Context) error {
	path := ctx.Request.URL.Path
	if isPublicAuthPathForContext(ctx, path) {
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

	if f.TokenManager != nil {
		claims, err = f.TokenManager.ParseToken(tokenStr)
	} else if f.JWTUtil != nil {
		claims, err = f.JWTUtil.ParseToken(tokenStr)
	} else {
		return NewError(401, "invalid or expired token")
	}

	if err != nil {
		return NewError(401, "invalid or expired token")
	}

	// 将用户信息存入上下文
	ctx.Set("current_user_id", claims.UserID)
	ctx.Set("current_token", tokenStr) // Save token for logout
	return nil
}

func isPublicAuthPathForContext(ctx *gin.Context, path string) bool {
	if runtimeValue, exists := ctx.Get(runtimeContextKey); exists {
		if runtime, ok := runtimeValue.(*Runtime); ok && runtime != nil {
			return isPublicAuthPathForConfig(path, runtime.Config)
		}
		return false
	}
	return isPublicAuthPath(path)
}

func (f *AuthFairing) Name() string {
	return "AuthFairing"
}

func isPublicAuthPath(path string) bool {
	return isPublicAuthPathForConfig(path, GetByType[*SysConfig]())
}

func isPublicAuthPathForConfig(path string, config *SysConfig) bool {
	if config == nil || config.Auth == nil {
		return false
	}
	for _, pattern := range config.Auth.PublicPaths {
		if publicPathMatch(path, pattern) {
			return true
		}
	}
	return false
}

func publicPathMatch(path, pattern string) bool {
	if pattern == "" {
		return false
	}
	if pattern == path {
		return true
	}
	if strings.HasSuffix(pattern, "/*") {
		prefix := strings.TrimSuffix(pattern, "/*")
		return path == prefix || strings.HasPrefix(path, prefix+"/")
	}
	return false
}
