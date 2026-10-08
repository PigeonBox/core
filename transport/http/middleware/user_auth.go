package middleware

import (
	"context"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/pigeonbox/core/pkg/auth"
	pkgmw "github.com/pigeonbox/core/pkg/middleware"
)

const (
	// UserRoleAdmin 管理员角色
	UserRoleAdmin = "admin"
	// UserRoleUser 普通用户角色
	UserRoleUser = "user"
)

// isRevoked 检查 token 是否已被注销（logout 黑名单；与 pkg/middleware 同一存储）
func isRevoked(ctx context.Context, token string) bool {
	return auth.IsTokenRevoked(ctx, token)
}

// UserAuth 用户 JWT 认证中间件
// 验证 Bearer JWT token 并将用户信息注入到上下文
func UserAuth() app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		tokenString := extractBearerToken(c)
		if tokenString == "" {
			respondUnauthorized(c, "Authorization header is required")
			return
		}

		claims, err := auth.ParseToken(tokenString)
		if err != nil {
			respondUnauthorized(c, "Invalid or expired token")
			return
		}

		// 注销黑名单检查（logout 端点写入；此前 /api/v1 组未接黑名单，
		// 登出后 token 仍然可用）
		if isRevoked(ctx, tokenString) {
			respondUnauthorized(c, "Token has been revoked")
			return
		}

		// 身份复核（2026-10-08 加固）：与 pkg AuthMiddleware 共享 30s 缓存回查——
		// 此前本中间件只验签名+黑名单，封禁/降权/改密后的旧 token 在
		// /api/v1/user/requests* 上仍可用到自然过期
		if !pkgmw.IdentityFresh(ctx, claims) {
			respondUnauthorized(c, "Session is no longer valid")
			return
		}
		pkgmw.SetJWTClaims(c, claims)

		c.Next(ctx)
	}
}

// RequireAdmin 要求管理员权限的中间件
// 必须配合 UserAuth 或其他认证中间件使用
func RequireAdmin() app.HandlerFunc {
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

// RequireRole 要求特定角色的中间件
// 必须配合 UserAuth 或其他认证中间件使用
func RequireRole(role string) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		if !IsAuthenticated(c) {
			respondUnauthorized(c, "Authentication required")
			return
		}

		if GetUserRole(c) != role {
			respondForbidden(c, "Required role: "+role)
			return
		}

		c.Next(ctx)
	}
}

// RequireAnyRole 要求具备任一指定角色的中间件
// 必须配合 UserAuth 或其他认证中间件使用
func RequireAnyRole(roles ...string) app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		if !IsAuthenticated(c) {
			respondUnauthorized(c, "Authentication required")
			return
		}

		currentRole := GetUserRole(c)
		for _, role := range roles {
			if currentRole == role {
				c.Next(ctx)
				return
			}
		}

		respondForbidden(c, "Access denied: insufficient permissions")
	}
}
