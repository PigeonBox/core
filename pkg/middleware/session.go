package middleware

// 会话 Cookie（2026-10-05 遗留修复：JWT 移出 localStorage）。
//
// 设计：HttpOnly Cookie 承载浏览器会话，令牌不再落入 JS 可达存储；Bearer 头
// 继续全量支持——API Key、脚本、桌面端、MCP 等 API 消费方零改动。
//
// CSRF 防御：Cookie 会被浏览器在同站请求中自动携带，跨站伪造请求须以自定义
// 头拦截（跨站请求无法携带自定义头，除非 CORS 放行——本站 CORS 为精确白名单、
// 默认为空）。约定：经 Cookie 认证的非安全方法（POST/PUT/DELETE 等）必须携带
// X-Requested-With: XMLHttpRequest；经 Bearer 认证的请求不受此限。
// Cookie 属性：HttpOnly + SameSite=Lax + host-only（不设 Domain）；Secure 仅在
// X-Forwarded-Proto=https 时置位（直连 http 的内网/家庭部署不因此锁死）。

import (
	"strings"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
)

const (
	// SessionCookieName 会话 Cookie 名（host-only，不设 Domain）
	SessionCookieName = "pb_token"
	// CSRFHeaderName Cookie 认证下非安全方法的必备请求头
	CSRFHeaderName = "X-Requested-With"
	// CSRFHeaderValue 该请求头的期望值
	CSRFHeaderValue = "XMLHttpRequest"
)

// SetSessionCookie 下发会话 Cookie（maxAgeSeconds<=0 视为会话期 Cookie）。
func SetSessionCookie(c *app.RequestContext, token string, maxAgeSeconds int) {
	secure := strings.EqualFold(string(c.Request.Header.Peek("X-Forwarded-Proto")), "https")
	c.SetCookie(SessionCookieName, token, maxAgeSeconds, "/", "", protocol.CookieSameSiteLaxMode, secure, true)
}

// ClearSessionCookie 清除会话 Cookie（logout/会话失效时调用）。
func ClearSessionCookie(c *app.RequestContext) {
	c.SetCookie(SessionCookieName, "", -1, "/", "", protocol.CookieSameSiteLaxMode, false, true)
}

// SessionToken 提取会话令牌：Authorization: Bearer 头优先，回退会话 Cookie。
// 返回 (token, viaCookie)。
func SessionToken(c *app.RequestContext) (string, bool) {
	if h := string(c.GetHeader("Authorization")); strings.HasPrefix(h, "Bearer ") {
		return strings.TrimSpace(strings.TrimPrefix(h, "Bearer ")), false
	}
	if bs := c.Cookie(SessionCookieName); len(bs) > 0 {
		return string(bs), true
	}
	return "", false
}

// CSRFAllowed Cookie 认证的 CSRF 门禁：安全方法（GET/HEAD/OPTIONS）放行；
// 非安全方法要求携带自定义头 X-Requested-With: XMLHttpRequest。
func CSRFAllowed(c *app.RequestContext, viaCookie bool) bool {
	if !viaCookie {
		return true
	}
	switch string(c.Method()) {
	case consts.MethodGet, consts.MethodHead, consts.MethodOptions:
		return true
	}
	return strings.EqualFold(string(c.GetHeader(CSRFHeaderName)), CSRFHeaderValue)
}
