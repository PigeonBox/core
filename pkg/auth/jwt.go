package auth

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

var (
	ErrInvalidToken = errors.New("invalid token")
	ErrExpiredToken = errors.New("token has expired")
	ErrTokenRevoked = errors.New("token has been revoked")

	jwtSecretMu sync.RWMutex
	jwtSecret   = []byte("FileCodeBox2025SecretKey")
)

// getSecret 读锁获取当前密钥（并发安全）
func getSecret() []byte {
	jwtSecretMu.RLock()
	defer jwtSecretMu.RUnlock()
	return jwtSecret
}

// Claims JWT claims
type Claims struct {
	UserID   uint   `json:"user_id"`
	Username string `json:"username"`
	Role     string `json:"role"`
	jwt.RegisteredClaims
}

// sessionExpiry 会话时长，默认 7 天。
// 管理后台"用户配置→会话过期时间"通过 SetSessionExpiry 在线调整
//（此前硬编码 168h，配置项形同虚设）。
var sessionExpiry = 7 * 24 * time.Hour

// SetSessionExpiry 设置会话时长；<=0 视为非法，保留原值。
// 仅影响新签发的 token，已签发会话到自然过期。
func SetSessionExpiry(d time.Duration) {
	if d <= 0 {
		return
	}
	sessionExpiry = d
}

// SessionExpiry 当前会话时长（测试/诊断用）。
func SessionExpiry() time.Duration { return sessionExpiry }

// GenerateToken 生成 JWT token
func GenerateToken(userID uint, username, role string) (string, error) {
	claims := &Claims{
		UserID:   userID,
		Username: username,
		Role:     role,
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(sessionExpiry)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Issuer:    "FileCodeBox",
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	return token.SignedString(getSecret())
}

// ParseToken 解析 JWT token
func ParseToken(tokenString string) (*Claims, error) {
	secret := getSecret()
	token, err := jwt.ParseWithClaims(tokenString, &Claims{}, func(token *jwt.Token) (interface{}, error) {
		return secret, nil
	})
	if err != nil {
		if errors.Is(err, jwt.ErrTokenExpired) {
			return nil, ErrExpiredToken
		}
		return nil, ErrInvalidToken
	}
	if claims, ok := token.Claims.(*Claims); ok && token.Valid {
		return claims, nil
	}
	return nil, ErrInvalidToken
}

// RefreshToken 刷新 token。已注销（黑名单）的 token 拒绝刷新，
// 堵住"登出后旧 token 仍可换新"的撤销绕过（设计文档 §9.6）。
func RefreshToken(ctx context.Context, tokenString string) (string, error) {
	claims, err := ParseToken(tokenString)
	if err != nil {
		return "", err
	}
	if IsTokenRevoked(ctx, tokenString) {
		return "", ErrTokenRevoked
	}
	return GenerateToken(claims.UserID, claims.Username, claims.Role)
}

// GenerateAdminToken 生成管理员 token
func GenerateAdminToken(userID uint, username string) (string, error) {
	return GenerateToken(userID, username, "admin")
}

// SetJWTSecret 设置 JWT secret（写锁，从配置注入）
func SetJWTSecret(secret string) {
	jwtSecretMu.Lock()
	defer jwtSecretMu.Unlock()
	jwtSecret = []byte(secret)
}
