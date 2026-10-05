package middleware

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/stretchr/testify/assert"
)

// runSecurityHeaders 构造带指定请求路径/请求头的 RequestContext 跑一遍中间件
func runSecurityHeaders(t *testing.T, path string) *app.RequestContext {
	t.Helper()
	c := app.NewContext(1)
	c.Request.SetRequestURI(path)
	SecurityHeaders()(context.Background(), c)
	return c
}

// TestSecurityHeadersStripsServerFingerprint 框架指纹不得下发
func TestSecurityHeadersStripsServerFingerprint(t *testing.T) {
	c := app.NewContext(1)
	c.Request.SetRequestURI("/ping")
	// 模拟 hertz 在 handler 链前写入的 Server 头
	c.Response.Header.Set("Server", "hertz")
	SecurityHeaders()(context.Background(), c)
	assert.Empty(t, string(c.Response.Header.Get("Server")), "Server 指纹头必须被剥离")
}

// TestSecurityHeadersNoStore 动态响应禁缓存 + /assets 豁免
func TestSecurityHeadersNoStore(t *testing.T) {
	c := runSecurityHeaders(t, "/api/config")
	assert.Equal(t, "no-store", string(c.Response.Header.Get("Cache-Control")),
		"动态响应必须 no-store（防缓存泄露私有数据）")

	a := runSecurityHeaders(t, "/assets/index-abc123.js")
	assert.NotEqual(t, "no-store", string(a.Response.Header.Get("Cache-Control")),
		"/assets/ 哈希命名构建产物应豁免，保留可缓存性")

	// handler 后续 Set 覆盖能力（下载类自定义缓存策略）
	c2 := app.NewContext(1)
	c2.Request.SetRequestURI("/share/download")
	SecurityHeaders()(context.Background(), c2)
	c2.Header("Cache-Control", "private, max-age=0")
	assert.Equal(t, "private, max-age=0", string(c2.Response.Header.Get("Cache-Control")),
		"后续 handler 应能覆盖默认 no-store")
}

// TestSecurityHeadersBaseline 基础安全头齐备
func TestSecurityHeadersBaseline(t *testing.T) {
	c := runSecurityHeaders(t, "/ping")
	for _, h := range []string{"X-Content-Type-Options", "X-Frame-Options", "X-XSS-Protection", "Referrer-Policy"} {
		assert.NotEmpty(t, string(c.Response.Header.Get(h)), "%s 必须存在", h)
	}
	assert.Empty(t, string(c.Response.Header.Get("Strict-Transport-Security")),
		"HSTS 默认关闭（仅 HTTPS 部署显式开启）")
	SetSecurityHeadersConfig(SecurityHeadersConfig{EnableHSTS: true})
	c2 := runSecurityHeaders(t, "/ping")
	assert.True(t, strings.HasPrefix(string(c2.Response.Header.Get("Strict-Transport-Security")), "max-age="))
	SetSecurityHeadersConfig(SecurityHeadersConfig{EnableHSTS: false})
}
