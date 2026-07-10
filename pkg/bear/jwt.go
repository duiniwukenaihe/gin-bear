package bear

import (
	"errors"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// JWTConfig JWT 配置
type JWTConfig struct {
	Secret  string `yaml:"secret"`
	Expires int    `yaml:"expires"` // 小时
}

// CustomClaims 自定义载荷
type CustomClaims struct {
	UserID uint   `json:"user_id"`
	Email  string `json:"email"`
	jwt.RegisteredClaims
}

// JWTUtil JWT 工具类
type JWTUtil struct {
	Config *JWTConfig `inject:"-"`
}

func NewJWTUtil(secret string, expires int) *JWTUtil {
	return &JWTUtil{Config: &JWTConfig{Secret: secret, Expires: expires}}
}

// GenerateToken 生成 Token
func (j *JWTUtil) GenerateToken(userID uint, email string) (string, error) {
	claims := CustomClaims{
		UserID: userID,
		Email:  email,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(time.Duration(j.Config.Expires) * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			NotBefore: jwt.NewNumericDate(time.Now()),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(j.Config.Secret))
}

// ParseToken 解析 Token
func (j *JWTUtil) ParseToken(tokenStr string) (*CustomClaims, error) {
	token, err := jwt.ParseWithClaims(tokenStr, &CustomClaims{}, func(token *jwt.Token) (interface{}, error) {
		if token.Method != jwt.SigningMethodHS256 {
			return nil, errors.New("unexpected signing method")
		}
		return []byte(j.Config.Secret), nil
	})

	if err != nil {
		return nil, err
	}

	if claims, ok := token.Claims.(*CustomClaims); ok && token.Valid {
		return claims, nil
	}

	return nil, errors.New("invalid token")
}

func (j *JWTUtil) Name() string {
	return "JWTUtil"
}
