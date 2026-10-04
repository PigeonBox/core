package middleware

import (
	"context"

	"github.com/cloudwego/hertz/pkg/app"
)

// AdminAuth 管理员 JWT 认证中间件
// 验证 JWT token 并确保用户具有管理员角色
func AdminAuth() app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		tokenString := extractBearerToken(c)
		if tokenString == "" {
			respondUnauthorized(c, "Authorization header is required")
			return
		}

		if err := parseAndSetClaims(c, tokenString); err != nil {
			respondUnauthorized(c, "Invalid or expired token")
			return
		}

		// 检查管理员权限
		if !IsAdmin(c) {
			respondForbidden(c, "Admin access required")
			return
		}

		c.Next(ctx)
	}
}

// AdminAuthStrict/SuperAdminAuth/AdminOrSystemAdmin 三个纯别名已删除：
// 与 AdminAuth 行为完全一致且全库零调用（"预留接口"永不兑现，YAGNI）。

// AdminOnly 仅允许管理员访问（不检查 JWT，从上下文获取角色）
// 用于在已认证的基础上二次验证管理员权限
func AdminOnly() app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		if !IsAuthenticated(c) {
			respondUnauthorized(c, "Authentication required")
			return
		}

		if !IsAdmin(c) {
			respondForbidden(c, "Admin access required")
			return
		}

		c.Next(ctx)
	}
}
