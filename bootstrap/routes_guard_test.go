package bootstrap

import (
	"fmt"
	"testing"

	"github.com/cloudwego/hertz/pkg/app/server"

	"github.com/filescodebox/core/conf"
	"github.com/filescodebox/core/gen/router"
)

// TestRegisteredRoutesMatchContract 契约守卫（学习上游 API 契约守卫测试）：
// 以"期望路由表"为契约快照，同时做双向守卫——
//  1. 契约内的每条路由必须已注册（防"IDL 有、路由漏"）；
//  2. 实际注册的路由不得超出契约 ∪ 已知白名单（防手写路由静默漂移，
//     尤其防未受保护的管理端点被顺手加进来）。
//
// 路由有意的增删必须同步修改本文件的 expected 表并过 CI 评审。
func TestRegisteredRoutesMatchContract(t *testing.T) {
	// 最小全局配置：customizedRegister 注册阶段读 Observability 开关
	// （bootstrap 包用自身 config 包级变量，BootstrapWithOptions 才赋值）
	prev := config
	config = &conf.AppConfiguration{MCP: conf.MCPConfig{Enabled: true}}
	defer func() { config = prev }()

	h := server.New(server.WithHostPorts("127.0.0.1:0"))
	router.GeneratedRegister(h)
	customizedRegister(h)

	actual := map[string]bool{}
	for _, r := range h.Routes() {
		actual[r.Method+" "+r.Path] = true
	}

	// expected：IDL 生成路由 + customizedRegister 手写路由的完整契约
	expected := []string{
		// ===== admin（IDL）=====
		"GET /admin/config", "PUT /admin/config",
		"GET /admin/files", "DELETE /admin/files/:id",
		"POST /admin/login", "GET /admin/stats",
		"GET /admin/users", "PUT /admin/users/:id/status",
		// ===== admin 增强（手写，均受 AdminMiddleware 保护）=====
		"GET /admin/activities",
		"POST /admin/users", "PUT /admin/users/:id", "DELETE /admin/users/:id",
		"POST /admin/users/:id/reset-password", "GET /admin/users/filter",
		"GET /admin/files/:id", "PUT /admin/files/:id",
		"GET /admin/files/:id/download",
		"POST /admin/files/batch-delete", "POST /admin/files/batch-extend",
		// 分享治理（组合过滤 + 状态机）
		"GET /admin/files/filter",
		"PUT /admin/files/:id/status", "POST /admin/files/batch-status",
		"GET /admin/stats/enhanced", "GET /admin/stats/trend",
		"GET /admin/logs/transfer",
		// ===== maintenance（IDL）=====
		"POST /admin/maintenance/clean-expired", "POST /admin/maintenance/clean-temp",
		"GET /admin/maintenance/logs", "GET /admin/maintenance/system-info",
		"GET /admin/maintenance/monitor/storage",
		// ===== ratelimit（IDL）=====
		"GET /admin/ratelimit/config", "PUT /admin/ratelimit/config",
		"GET /admin/ratelimit/status", "POST /admin/ratelimit/test",
		// ===== storage（IDL）=====
		"GET /admin/storage", "PUT /admin/storage/config",
		"POST /admin/storage/switch", "GET /admin/storage/test/:type",
		// ===== notify（IDL）=====
		"GET /admin/notifies", "POST /admin/notifies",
		"GET /admin/notifies/:id", "PUT /admin/notifies/:id", "DELETE /admin/notifies/:id",
		"GET /notifies/active",
		// ===== user（IDL）=====
		"GET /user/api-keys", "DELETE /user/api-keys/:id", "POST /user/api-keys",
		"POST /user/change-password", "GET /user/files", "GET /user/info",
		"POST /user/login", "PUT /user/profile", "POST /user/register", "GET /user/stats",
		// ===== share（IDL）=====
		"GET /share/download", "POST /share/file/", "GET /share/select/", "POST /share/text/",
		// ===== anonymous（IDL）=====
		"POST /anonymous/generate", "POST /anonymous/retrieve",
		"GET /anonymous/download/:code", "GET /anonymous/search/:code",
		// ===== chunk（IDL）=====
		"POST /chunk/upload/init/",
		"POST /chunk/upload/chunk/:upload_id/:chunk_index",
		"POST /chunk/upload/complete/:upload_id",
		"DELETE /chunk/upload/cancel/:upload_id",
		"GET /chunk/upload/status/:upload_id",
		// ===== presign（IDL + 手写直传端点）=====
		"POST /api/v1/presign/upload", "POST /api/v1/presign/complete", "POST /api/v1/presign/abort",
		"PUT /api/v1/presign/upload-direct/:uploadID",
		// ===== preview / qrcode / setup / health / common（IDL）=====
		"GET /preview/:code",
		"POST /qrcode/generate", "GET /qrcode/:id",
		"POST /setup", "GET /setup/check",
		"GET /health", "GET /live", "GET /ping", "GET /ready", "GET /version",
		"GET /api/v1/ping",
		// ===== customizedRegister 手写 =====
		"GET /openapi.json", "GET /api/config", "GET /robots.txt",
		"POST /api/v1/mcp",
		"POST /api/v1/user/refresh", "POST /api/v1/user/logout", "GET /api/v1/user/check-auth",
		"GET /readyz",
		"GET /api/v1/user/shares", "POST /api/v1/user/shares/batch-delete",
		"POST /api/v1/user/shares/batch-extend", "POST /api/v1/user/shares/:code/restore",
		"DELETE /api/v1/user/shares/:code/hard",
		"GET /api/v1/notifies/mine", "GET /api/v1/notifies/unread-count",
		"POST /api/v1/notifies/mark-read",
	}

	// 1. 契约内路由必须全部已注册
	missing := []string{}
	for _, r := range expected {
		if !actual[r] {
			missing = append(missing, r)
		}
	}
	if len(missing) > 0 {
		t.Fatalf("契约路由缺失 %d 条（IDL 声明了但未注册，或手写路由被误删）:\n  %s",
			len(missing), joinLines(missing))
	}

	// 2. 实际路由不得超出契约 ∪ 白名单（静态资源等框架自动注册项）
	allowedExtra := map[string]bool{
		"GET /assets/*filepath":  true, // StaticFS 前端资源
		"HEAD /assets/*filepath": true,
	}
	unexpected := []string{}
	for r := range actual {
		if allowedExtra[r] {
			continue
		}
		found := false
		for _, e := range expected {
			if e == r {
				found = true
				break
			}
		}
		if !found {
			unexpected = append(unexpected, r)
		}
	}
	if len(unexpected) > 0 {
		t.Fatalf("发现契约外路由 %d 条（新路由须同步更新本测试的契约表）:\n  %s",
			len(unexpected), joinLines(unexpected))
	}
}

func joinLines(items []string) string {
	out := ""
	for i, s := range items {
		if i > 0 {
			out += "\n  "
		}
		out += fmt.Sprintf("- %s", s)
	}
	return out
}
