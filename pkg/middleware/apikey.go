package middleware

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/filescodebox/contracts/errcode"
	"github.com/filescodebox/core/conf"
	"github.com/filescodebox/core/repo/db/dao"
)

// apiKeyPlainPrefix API Key 明文前缀（与 app/user 签发端一致）。
// 同时承担 Bearer 凭证与 JWT 的确定性区分：fcb_sk_ 开头必为 Key（JWT 恒为 eyJ 开头）。
const apiKeyPlainPrefix = "fcb_sk_"

// errInvalidAPIKey 统一拒绝原因（对外一律 401 "Invalid API Key"，不区分过期/吊销/封禁，防枚举）。
var errInvalidAPIKey = errors.New("invalid api key")

// errAPITokenDisabled 认证总开关关闭（security.api_token.enabled=false，紧急停用）。
var errAPITokenDisabled = errors.New("api token auth disabled")

// apiTokenEnabled API Key 认证总开关；无全局配置（单测等）默认开启。
func apiTokenEnabled() bool {
	if c := conf.GetGlobalConfig(); c != nil {
		return c.Security.APIToken.Enabled
	}
	return true
}

// extractAPIKey 从请求头提取 API Key。拒绝 query 传参（防访问日志/Referer/代理日志泄露）。
// 支持三种形式：
//  1. Authorization: Bearer fcb_sk_xxx（首选）
//  2. Authorization: ApiKey xxx
//  3. X-API-Key: xxx
//
// 返回 (key, true) 表示请求显式携带 Key（无论是否有效，调用方须 fail-closed）；
// 返回 ("", false) 表示未携带 Key（按匿名/JWT 语义处理）。
func extractAPIKey(c *app.RequestContext) (string, bool) {
	authHeader := strings.TrimSpace(string(c.GetHeader("Authorization")))
	if authHeader != "" {
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) == 2 {
			token := strings.TrimSpace(parts[1])
			if token != "" && strings.EqualFold(parts[0], "ApiKey") {
				return token, true
			}
			if parts[0] == "Bearer" && strings.HasPrefix(token, apiKeyPlainPrefix) {
				return token, true
			}
		}
	}
	if key := strings.TrimSpace(string(c.GetHeader("X-API-Key"))); key != "" {
		return key, true
	}
	return "", false
}

// sha256Hex Key 摘要（与 app/user.HashAPIKey 同算法：hex(SHA-256)）
func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

// ===== TouchLastUsed 写节流 =====

// touchInterval 每 Key 两次 last_used_at 落库的最小间隔。
// 多实例各自节流即可（目的仅是降写放大，非精确统计）。
const touchInterval = time.Minute

var touchThrottle sync.Map // keyID(uint) → last touch time.Time

func shouldTouchLastUsed(keyID uint, now time.Time) bool {
	if v, ok := touchThrottle.Load(keyID); ok && now.Sub(v.(time.Time)) < touchInterval {
		return false
	}
	touchThrottle.Store(keyID, now)
	return true
}

// ===== 校验与身份注入 =====

// validateAPIKey 校验明文 Key 并注入身份（与 JWT 中间件双写方言一致：
// c.Set 供 handler，ctx 注入供 service 层 UserIDFromContext 审计提取）。
// 防护链：lockout(apikey|IP 防爆破) → 摘要查库(吊销/过期) → 用户状态 → 触碰节流。
// 失败一律返回 errInvalidAPIKey 或 *LockedError，不泄露具体原因。
// 认证查询不走缓存：吊销/封禁必须即时生效（每请求 2 个索引查询，亚毫秒级）。
func validateAPIKey(ctx context.Context, c *app.RequestContext, plainKey string) (context.Context, error) {
	// 总开关：紧急停用时不做任何校验/计数，携带 Key 一律 401（fail-closed）
	if !apiTokenEnabled() {
		return ctx, errAPITokenDisabled
	}

	lock := GetDefaultLockout()
	lockKey := FormatLockKey("apikey", ClientIP(c))
	if remain, locked := lock.CheckLocked(ctx, lockKey); locked {
		return ctx, &LockedError{RemainingSeconds: remain}
	}

	keyRepo := dao.NewUserAPIKeyRepository()
	key, err := keyRepo.GetActiveByHash(ctx, sha256Hex(plainKey))
	if err != nil {
		_, _ = lock.RecordFailure(ctx, lockKey)
		return ctx, errInvalidAPIKey
	}

	user, err := dao.NewUserRepository().GetByID(ctx, key.UserID)
	if err != nil || user.Status != "active" {
		// 封禁/停用用户的 Key 视为无效 Key（同样计入防爆破）
		_, _ = lock.RecordFailure(ctx, lockKey)
		return ctx, errInvalidAPIKey
	}
	lock.Reset(ctx, lockKey)

	if shouldTouchLastUsed(key.ID, time.Now()) {
		_ = keyRepo.TouchLastUsed(ctx, key.ID)
	}

	c.Set("user_id", user.ID)
	c.Set("username", user.Username)
	c.Set("role", user.Role)
	c.Set("api_key_id", key.ID)   // 与 transport 侧 ContextKeyAPIKeyID 同字符串
	c.Set("auth_type", "api_key") // 与 transport 侧 ContextKeyAuthType 同字符串
	c.Header("X-User-ID", fmt.Sprintf("%d", user.ID))
	c.Header("X-Username", user.Username)
	c.Header("X-Role", user.Role)

	return withIdentity(ctx, user.ID, user.Username, user.Role, ClientIP(c), key.ID), nil
}

// respondAPIKeyError 统一错误响应：总开关关闭 → 401；被锁定 → 429；无效 → 401（统一文案防枚举）。
func respondAPIKeyError(c *app.RequestContext, err error) {
	if errors.Is(err, errAPITokenDisabled) {
		c.Abort()
		c.JSON(http.StatusUnauthorized, map[string]interface{}{
			"code":    http.StatusUnauthorized,
			"message": "API token authentication is disabled",
		})
		return
	}
	var le *LockedError
	if errors.As(err, &le) {
		c.Abort()
		c.JSON(http.StatusTooManyRequests, map[string]interface{}{
			"code":    errcode.CodeTooManyAttempts,
			"message": fmt.Sprintf("尝试过于频繁，已临时锁定，请 %d 秒后重试", le.RemainingSeconds),
		})
		return
	}
	c.Abort()
	c.JSON(http.StatusUnauthorized, map[string]interface{}{
		"code":    http.StatusUnauthorized,
		"message": "Invalid API Key",
	})
}

// ===== 对外中间件 =====

// OptionalAPIKey 可选 API Key 认证（fail-closed：携带即校验，无效一律 401/429）
func OptionalAPIKey() app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		key, present := extractAPIKey(c)
		if !present {
			c.Next(ctx)
			return
		}
		newCtx, err := validateAPIKey(ctx, c, key)
		if err != nil {
			respondAPIKeyError(c, err)
			return
		}
		c.Next(newCtx)
	}
}

// OptionalIdentity 可选身份认证组：JWT（既有可选语义）→ API Key（fail-closed）。
// 未携带凭证 → 匿名；携带 Key 但无效 → 401；JWT 无效 → 匿名（会话过期 UX，与 Key 有意不对称）。
// 供 gen 路由挂载点整体替换 OptionalAuthMiddleware()（挂载函数直接 return 本切片）。
func OptionalIdentity() []app.HandlerFunc {
	return []app.HandlerFunc{OptionalAuthMiddleware(), OptionalAPIKey()}
}

// UserOrAPIKey 登录凭证二选一：API Key 或 JWT（含黑名单检查，复用 AuthMiddleware 完整语义）。
// 供 /api/v1 自定义 REST 组替换 transport UserAuth，向第三方开放 Key 访问。
// 边界（设计文档 §4.1）：绝不挂 /admin；/user/api-keys 保持 JWT-only（Key 不能管 Key）。
func UserOrAPIKey() app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		if key, present := extractAPIKey(c); present {
			newCtx, err := validateAPIKey(ctx, c, key)
			if err != nil {
				respondAPIKeyError(c, err)
				return
			}
			c.Next(newCtx)
			return
		}
		AuthMiddleware()(ctx, c)
	}
}
