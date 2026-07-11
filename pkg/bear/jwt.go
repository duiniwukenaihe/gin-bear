package bear

import (
	"errors"
	"fmt"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// ErrInvalidTokenExpiration reports an invalid configured token lifetime.
var ErrInvalidTokenExpiration = errors.New("token expiration hours must be positive")

// JWTConfig JWT 配置
type JWTConfig struct {
	Secret    string        `yaml:"secret" json:"secret"`
	Expires   int           `yaml:"expires" json:"expires"` // 小时
	Issuer    string        `yaml:"issuer" json:"issuer"`
	Audience  string        `yaml:"audience" json:"audience"`
	ClockSkew time.Duration `yaml:"clock_skew" json:"clock_skew"`
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

func newJWTUtilFromAuthConfig(config *AuthConfig) *JWTUtil {
	if config == nil {
		return NewJWTUtil("", 0)
	}

	clockSkew := time.Duration(0)
	if config.JWTClockSkew != "" {
		clockSkew, _ = time.ParseDuration(config.JWTClockSkew)
	}
	return &JWTUtil{Config: &JWTConfig{
		Secret:    config.JWTSecret,
		Expires:   config.TokenExpireHours,
		Issuer:    config.JWTIssuer,
		Audience:  config.JWTAudience,
		ClockSkew: clockSkew,
	}}
}

// GenerateToken 生成 Token
func (j *JWTUtil) GenerateToken(userID uint, email string) (string, error) {
	if j == nil || j.Config == nil {
		return "", errors.New("jwt configuration is unavailable")
	}
	if err := validateJWTExpiration(j.Config); err != nil {
		return "", err
	}
	now := time.Now()
	claims := CustomClaims{
		UserID: userID,
		Email:  email,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(now.Add(time.Duration(j.Config.Expires) * time.Hour)),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
			Issuer:    j.Config.Issuer,
		},
	}
	if j.Config.Audience != "" {
		claims.Audience = jwt.ClaimStrings{j.Config.Audience}
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString([]byte(j.Config.Secret))
}

// ParseToken 解析 Token
func (j *JWTUtil) ParseToken(tokenStr string) (*CustomClaims, error) {
	if j == nil || j.Config == nil {
		return nil, errors.New("jwt configuration is unavailable")
	}
	if err := validateJWTExpiration(j.Config); err != nil {
		return nil, err
	}
	if j.Config.ClockSkew < 0 || j.Config.ClockSkew > 5*time.Minute {
		return nil, errors.New("jwt clock skew must be between 0 and 5m")
	}
	unverified, _, err := jwt.NewParser().ParseUnverified(tokenStr, &CustomClaims{})
	if err != nil {
		return nil, err
	}
	if unverified.Method != jwt.SigningMethodHS256 {
		return nil, errors.New("unexpected signing method")
	}
	options := []jwt.ParserOption{
		jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}),
		jwt.WithLeeway(j.Config.ClockSkew),
		jwt.WithExpirationRequired(),
	}
	if j.Config.Issuer != "" {
		options = append(options, jwt.WithIssuer(j.Config.Issuer))
	}
	if j.Config.Audience != "" {
		options = append(options, jwt.WithAudience(j.Config.Audience))
	}
	token, err := jwt.ParseWithClaims(tokenStr, &CustomClaims{}, func(token *jwt.Token) (interface{}, error) {
		if token.Method != jwt.SigningMethodHS256 {
			return nil, errors.New("unexpected signing method")
		}
		return []byte(j.Config.Secret), nil
	}, options...)

	if err != nil {
		return nil, err
	}

	if claims, ok := token.Claims.(*CustomClaims); ok && token.Valid {
		return claims, nil
	}

	return nil, errors.New("invalid token")
}

func validateJWTExpiration(config *JWTConfig) error {
	if config == nil {
		return errors.New("jwt configuration is unavailable")
	}
	return validateTokenExpirationHours(config.Expires)
}

func validateTokenExpirationHours(expires int) error {
	if expires > 0 {
		return nil
	}
	return fmt.Errorf("%w: got %d", ErrInvalidTokenExpiration, expires)
}

func (j *JWTUtil) Name() string {
	return "JWTUtil"
}
