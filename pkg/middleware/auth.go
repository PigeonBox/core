package middleware

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/filescodebox/core/pkg/auth"
)

// context key 类型（非导出，防止跨包键冲突）
type ctxKey int

const (
	ctxKeyUserID ctxKey = iota
	ctxKeyUsername
	ctxKeyRole
	ctxKeyClientIP
	ctxKeyAPIKeyID
)

// 注入/读取工具：中间件将认证信息写入 ctx，service 层经此提取操作者（审计用）。
// apiKeyID 非 0 表示本次请求经用户级 API Key 认证（写入传输日志做 Key 粒度归因）。
func withIdentity(ctx context.Context, userID uint, username, role, ip string, apiKeyID uint) context.Context {
	ctx = context.WithValue(ctx, ctxKeyUserID, userID)
	ctx = context.WithValue(ctx, ctxKeyUsername, username)
	ctx = context.WithValue(ctx, ctxKeyRole, role)
	ctx = context.WithValue(ctx, ctxKeyClientIP, ip)
	if apiKeyID > 0 {
		ctx = context.WithValue(ctx, ctxKeyAPIKeyID, apiKeyID)
	}
	return ctx
}

// APIKeyIDFromContext 从 ctx 读取认证所用的 API Key ID（JWT/匿名请求返回 false）。
func APIKeyIDFromContext(ctx context.Context) (uint, bool) {
	v, ok := ctx.Value(ctxKeyAPIKeyID).(uint)
	return v, ok
}

// UserIDFromContext 从 ctx 读取用户 ID（未认证返回 false）
func UserIDFromContext(ctx context.Context) (uint, bool) {
	v, ok := ctx.Value(ctxKeyUserID).(uint)
	return v, ok
}

// UsernameFromContext 从 ctx 读取用户名（未认证返回空串）
func UsernameFromContext(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyUsername).(string)
	return v
}

// RoleFromContext 从 ctx 读取角色
func RoleFromContext(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyRole).(string)
	return v
}

// ClientIPFromContext 从 ctx 读取已解析的客户端 IP
func ClientIPFromContext(ctx context.Context) string {
	v, _ := ctx.Value(ctxKeyClientIP).(string)
	return v
}

// AuthMiddleware JWT认证中间件
func AuthMiddleware() app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		// 获取Authorization头
		authHeader := string(c.GetHeader("Authorization"))
		if authHeader == "" {
			c.JSON(http.StatusUnauthorized, map[string]interface{}{
				"code":    http.StatusUnauthorized,
				"message": "Authorization header is required",
			})
			c.Abort()
			return
		}

		// 验证Bearer token格式
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || parts[0] != "Bearer" {
			c.JSON(http.StatusUnauthorized, map[string]interface{}{
				"code":    http.StatusUnauthorized,
				"message": "Authorization header format must be Bearer {token}",
			})
			c.Abort()
			return
		}

		// 解析JWT token
		claims, err := auth.ParseToken(parts[1])
		if err != nil {
			c.JSON(http.StatusUnauthorized, map[string]interface{}{
				"code":    http.StatusUnauthorized,
				"message": "Invalid or expired token",
			})
			c.Abort()
			return
		}

		// 注销黑名单检查（logout 端点写入；已注销 token 即刻失效）
		if auth.IsTokenRevoked(ctx, parts[1]) {
			c.JSON(http.StatusUnauthorized, map[string]interface{}{
				"code":    http.StatusUnauthorized,
				"message": "Token has been revoked",
			})
			c.Abort()
			return
		}

		// 将用户信息存储到上下文中（RequestContext + ctx 双写：
		// c.Get 供 handler 用，ctx value 供 service 层提取审计操作者）
		c.Set("user_id", claims.UserID)
		c.Set("username", claims.Username)
		c.Set("role", claims.Role)
		ctx = withIdentity(ctx, claims.UserID, claims.Username, claims.Role, ClientIP(c), 0)

		// 同时设置 Header，方便 handler 读取
		c.Header("X-User-ID", fmt.Sprintf("%d", claims.UserID))
		c.Header("X-Username", claims.Username)
		c.Header("X-Role", claims.Role)

		c.Next(ctx)
	}
}

// AdminMiddleware 管理员权限中间件
func AdminMiddleware() app.HandlerFunc {
	// 不需要认证的路径白名单
	skipPaths := map[string]bool{
		"/admin/login": true,
	}

	return func(ctx context.Context, c *app.RequestContext) {
		// 检查是否在白名单中
		if skipPaths[string(c.URI().Path())] {
			c.Next(ctx)
			return
		}

		// 先进行身份认证
		AuthMiddleware()(ctx, c)
		if c.IsAborted() {
			return
		}

		// 检查是否为管理员
		role, _ := c.Get("role")
		if role != "admin" {
			c.JSON(http.StatusForbidden, map[string]interface{}{
				"code":    http.StatusForbidden,
				"message": "Admin access required",
			})
			c.Abort()
			return
		}

		c.Next(ctx)
	}
}

// OptionalAuthMiddleware 可选认证中间件（不强制要求登录）
func OptionalAuthMiddleware() app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		// 获取Authorization头
		authHeader := string(c.GetHeader("Authorization"))
		if authHeader == "" {
			c.Next(ctx)
			return
		}

		// 验证Bearer token格式
		parts := strings.SplitN(authHeader, " ", 2)
		if len(parts) != 2 || parts[0] != "Bearer" {
			c.Next(ctx)
			return
		}

		// 解析JWT token
		claims, err := auth.ParseToken(parts[1])
		if err != nil {
			c.Next(ctx)
			return
		}

		// 将用户信息存储到上下文中
		c.Set("user_id", claims.UserID)
		c.Set("username", claims.Username)
		c.Set("role", claims.Role)

		// 同时设置 Header
		c.Header("X-User-ID", fmt.Sprintf("%d", claims.UserID))
		c.Header("X-Username", claims.Username)
		c.Header("X-Role", claims.Role)

		c.Next(ctx)
	}
}
