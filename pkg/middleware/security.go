package middleware

import (
	"context"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"
)

// SecurityHeadersConfig 安全响应头中间件的配置项。
//
//   - X-Content-Type-Options: nosniff  → 禁止浏览器嗅探 MIME 类型
//   - X-Frame-Options: SAMEORIGIN      → 防止点击劫持（页面只能被同源 iframe 嵌入）
//   - X-XSS-Protection: 1; mode=block  → 启用浏览器 XSS 过滤器（旧浏览器）
//   - Referrer-Policy: strict-origin-when-cross-origin → 限制 Referrer 泄露
//   - Strict-Transport-Security        → 仅在生产(HTTPS)下启用 HSTS
//   - Cache-Control: no-store          → 动态响应禁缓存（防中间盒子/浏览器缓存
//     泄露会话与私有数据）；/assets/ 哈希命名构建产物豁免
//
// 另：剥离 Server 指纹头（hertz 在进入 handler 前已写入 "Server: hertz"，
// 框架名帮助攻击者对号入座找现成漏洞利用，删除无副作用）。
//
// 可通过 SetSecurityHeadersConfig 调整。
type SecurityHeadersConfig struct {
	// EnableHSTS 是否启用 Strict-Transport-Security（仅 HTTPS 部署时启用，
	// 否则可能把用户锁在无法访问的 https 状态）。
	EnableHSTS bool
}

var securityHeadersConfig = SecurityHeadersConfig{
	EnableHSTS: false, // 默认关闭，由生产配置显式开启
}

// SetSecurityHeadersConfig 设置安全头全局配置（bootstrap 调用）。
func SetSecurityHeadersConfig(cfg SecurityHeadersConfig) {
	securityHeadersConfig = cfg
}

// SecurityHeaders 安全响应头中间件。
func SecurityHeaders() app.HandlerFunc {
	return func(ctx context.Context, c *app.RequestContext) {
		// 框架指纹剥离（hertz 每请求在 handler 链前写入，此处删除即不再下发）
		c.Response.Header.Del("Server")
		// 动态响应默认禁缓存；静态构建产物（/assets/ 哈希文件名）豁免，
		// 文件下载等需要自定义缓存策略的 handler 在后续链中 Set 覆盖即可
		if !strings.HasPrefix(string(c.Request.URI().Path()), "/assets/") {
			c.Header("Cache-Control", "no-store")
		}
		c.Header("X-Content-Type-Options", "nosniff")
		c.Header("X-Frame-Options", "SAMEORIGIN")
		c.Header("X-XSS-Protection", "1; mode=block")
		c.Header("Referrer-Policy", "strict-origin-when-cross-origin")
		if securityHeadersConfig.EnableHSTS {
			// max-age=31536000（1年），含子域名，预加载
			c.Header("Strict-Transport-Security", "max-age=31536000; includeSubDomains; preload")
		}
		c.Next(ctx)
	}
}
