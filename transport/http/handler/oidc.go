// OIDC SSO HTTP 端点（P2，手写路由）。
package handler

import (
	"context"
	"fmt"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	oidcApp "github.com/filescodebox/core/app/oidc"
	"github.com/filescodebox/core/conf"
)

var oidcSvc *oidcApp.Service

// SetOIDCService 注入 OIDC 服务（bootstrap 调用）
func SetOIDCService(s *oidcApp.Service) { oidcSvc = s }

func getOIDCService() *oidcApp.Service { return oidcSvc }

// getOIDCBaseURL 对外基础地址（与 bootstrap 同策略：base_url 优先）
func getOIDCBaseURL() string {
	if cfg := conf.GetGlobalConfig(); cfg != nil {
		if cfg.Server.BaseURL != "" {
			return cfg.Server.BaseURL
		}
		return fmt.Sprintf("http://%s:%d", cfg.Server.Host, cfg.Server.Port)
	}
	return "http://localhost:12345"
}

// redirectOIDCError 302 回前端登录页并携带错误信息（query 传中文需转义）
func redirectOIDCError(c *app.RequestContext, msg string) {
	c.Header("Cache-Control", "no-store")
	c.Redirect(consts.StatusFound, []byte("/#/user/login?oidc_error=1"))
}

// OIDCLogin 302 跳转到 IdP 授权页（GET /api/v1/user/oidc/login）
func OIDCLogin(ctx context.Context, c *app.RequestContext) {
	svc := getOIDCService()
	if svc == nil || !svc.Enabled() {
		c.JSON(consts.StatusNotFound, map[string]interface{}{"code": 404, "message": "OIDC 登录未启用"})
		return
	}
	baseURL := getOIDCBaseURL()
	loginURL, err := svc.LoginURL(ctx, baseURL)
	if err != nil {
		c.JSON(consts.StatusBadGateway, map[string]interface{}{"code": 502, "message": err.Error()})
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Redirect(consts.StatusFound, []byte(loginURL))
}

// OIDCCallback IdP 回调：换发本站 JWT 并 302 回前端（GET /api/v1/user/oidc/callback）
func OIDCCallback(ctx context.Context, c *app.RequestContext) {
	svc := getOIDCService()
	if svc == nil || !svc.Enabled() {
		c.JSON(consts.StatusNotFound, map[string]interface{}{"code": 404, "message": "OIDC 登录未启用"})
		return
	}
	code := c.Query("code")
	state := c.Query("state")
	if code == "" {
		redirectOIDCError(c, "授权失败：缺少 code")
		return
	}
	token, err := svc.ExchangeCallback(ctx, getOIDCBaseURL(), code, state)
	if err != nil {
		redirectOIDCError(c, err.Error())
		return
	}
	c.Header("Cache-Control", "no-store")
	c.Redirect(consts.StatusFound, []byte(svc.FrontendCallback()+token))
}
