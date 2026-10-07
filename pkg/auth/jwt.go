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
	jwtSecret   = []byte("PigeonBox2025SecretKey")
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
	// Epoch 会话纪元快照：签发时用户的 session_epoch。改密/封禁/降权会使
	// DB 纪元 +1，纪元不匹配的旧 token 即时失效（认证中间件回查比对）。
	Epoch int `json:"sess_epoch,omitempty"`
	jwt.RegisteredClaims
}

// epochLoader 签发时读取用户当前会话纪元（composition root 注入 dao 桥；
// 无注入时恒 0——纯单测环境语义不变）。
var (
	epochLoaderMu sync.RWMutex
	epochLoader   func(userID uint) int
)

// SetEpochLoader 注入纪元加载器（bootstrap 调用一次；nil 关闭）。
func SetEpochLoader(fn func(userID uint) int) {
	epochLoaderMu.Lock()
	defer epochLoaderMu.Unlock()
	epochLoader = fn
}

func currentEpoch(userID uint) int {
	epochLoaderMu.RLock()
	fn := epochLoader
	epochLoaderMu.RUnlock()
	if fn == nil {
		return 0
	}
	return fn(userID)
}

// sessionExpiry 会话时长，默认 7 天。
// 管理后台"用户配置→会话过期时间"通过 SetSessionExpiry 在线调整
// （此前硬编码 168h，配置项形同虚设）。
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
		Epoch:    currentEpoch(userID),
		RegisteredClaims: jwt.RegisteredClaims{
			ExpiresAt: jwt.NewNumericDate(time.Now().Add(sessionExpiry)),
			IssuedAt:  jwt.NewNumericDate(time.Now()),
			Issuer:    "PigeonBox",
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
	}, jwt.WithValidMethods([]string{"HS256"}))
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
//
// 轮换语义（2026-10-05 审计 P1 修复）：签发新 token 的同时把旧 token 按剩余
// 有效期加入黑名单——否则被盗 token 可与真用户并行使用到自然过期，且每次
// 刷新都无法止损。
func RefreshToken(ctx context.Context, tokenString string) (string, error) {
	claims, err := ParseToken(tokenString)
	if err != nil {
		return "", err
	}
	if IsTokenRevoked(ctx, tokenString) {
		return "", ErrTokenRevoked
	}
	newToken, err := GenerateToken(claims.UserID, claims.Username, claims.Role)
	if err != nil {
		return "", err
	}
	if remaining := time.Until(claims.ExpiresAt.Time); remaining > 0 {
		RevokeToken(ctx, tokenString, remaining)
	}
	return newToken, nil
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
