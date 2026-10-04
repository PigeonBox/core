// P2P 联邦解析代理（GET /api/v1/federation/resolve，手写路由）。
// 前端在录码本站未命中时经此查询联邦，命中则直跳源节点取件页预填口令；
// 本端点只做"发现"，不代理文件流量（下载始终直连源节点、源节点本地校验）。
// 未启用联邦时统一 available:false，不区分"未启用/不存在"（降探测面）；
// registry 侧对 resolve 限流最严，本端点不再叠加限流（M4 视压测调参）。
package handler

import (
	"context"

	"github.com/cloudwego/hertz/pkg/app"

	"github.com/filescodebox/core/app/federation"
	"github.com/filescodebox/core/pkg/resp"
)

var federationSvc *federation.Service

// SetFederationService 注入联邦服务（bootstrap 调用；nil = 未启用）
func SetFederationService(s *federation.Service) { federationSvc = s }

// FederationResolve 口令联邦解析。
func FederationResolve(ctx context.Context, c *app.RequestContext) {
	if federationSvc == nil {
		resp.Success(c, map[string]any{"available": false})
		return
	}
	code := string(c.Query("code"))
	if code == "" {
		resp.Success(c, map[string]any{"available": false})
		return
	}
	info, err := federationSvc.Resolve(code)
	if err != nil || info == nil {
		resp.Success(c, map[string]any{"available": false})
		return
	}
	resp.Success(c, map[string]any{
		"available":  true,
		"url":        info.URL,
		"node_id":    info.NodeID,
		"name":       info.Name,
		"expires_at": info.ExpiresAt.Unix(),
	})
}
