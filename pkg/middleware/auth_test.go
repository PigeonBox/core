package middleware

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/common/test/assert"
	"github.com/cloudwego/hertz/pkg/common/ut"
	"github.com/filescodebox/core/pkg/auth"
)

// AdminMiddleware 行为回归（2026-10-05 审计 P0）：
// 此前实现内联调用 AuthMiddleware()——其成功路径 c.Next 会把业务 handler 先
// 执行完，角色检查沦为"事后追认"：任意注册用户可执行全部 /admin/*（含建号/
// 改配置），读接口的业务响应体还会拼在 403 前面泄露。修复后必须：
//  1. 普通用户 token → 403，业务 handler 不执行（副作用为零）；
//  2. 伪造/无效 token → 401；
//  3. admin token → 放行，身份写入 RequestContext。
func TestAdminMiddlewareRoleGate(t *testing.T) {
	auth.SetJWTSecret("test-secret-for-admin-middleware-32chars")
	t.Cleanup(func() { auth.SetJWTSecret("") })

	userToken, err := auth.GenerateToken(2, "alice", "user")
	if err != nil {
		t.Fatal(err)
	}
	adminToken, err := auth.GenerateAdminToken(1, "admin")
	if err != nil {
		t.Fatal(err)
	}

	newServer := func(handlerRan *bool) *server.Hertz {
		h := server.New(server.WithHostPorts("127.0.0.1:0"))
		h.GET("/admin/secret", AdminMiddleware(), func(ctx context.Context, c *app.RequestContext) {
			*handlerRan = true
			c.JSON(http.StatusOK, map[string]string{"secret": "value"})
		})
		return h
	}

	// 1. 普通用户：403 且 handler 不执行
	ran := false
	h := newServer(&ran)
	w := ut.PerformRequest(h.Engine, http.MethodGet, "/admin/secret", nil,
		ut.Header{"Authorization", "Bearer " + userToken})
	resp := w.Result()
	assert.DeepEqual(t, http.StatusForbidden, resp.StatusCode())
	if ran {
		t.Fatal("角色检查前业务 handler 已被执行（P0 回归）")
	}
	if strings.Contains(string(resp.Body()), "secret") {
		t.Fatal("403 响应不得包含业务数据")
	}

	// 2. 无效 token：401
	ran = false
	h = newServer(&ran)
	w = ut.PerformRequest(h.Engine, http.MethodGet, "/admin/secret", nil,
		ut.Header{"Authorization", "Bearer not-a-jwt"})
	assert.DeepEqual(t, http.StatusUnauthorized, w.Result().StatusCode())
	if ran {
		t.Fatal("无效 token 下业务 handler 被执行")
	}

	// 3. admin token：200 且身份注入
	ran = false
	h = newServer(&ran)
	w = ut.PerformRequest(h.Engine, http.MethodGet, "/admin/secret", nil,
		ut.Header{"Authorization", "Bearer " + adminToken})
	assert.DeepEqual(t, http.StatusOK, w.Result().StatusCode())
	if !ran {
		t.Fatal("admin token 未放行业务 handler")
	}
	assert.DeepEqual(t, "admin", string(w.Result().Header.Get("X-Role")))
}

// OptionalAuthMiddleware 吊销语义回归（2026-10-05 审计 P1）：已注销 token 在
// 可选身份路由必须降级为匿名，不得继续注入 user_id。
func TestOptionalAuthMiddlewareRespectsBlacklist(t *testing.T) {
	auth.SetJWTSecret("test-secret-for-optional-auth-32ch")
	t.Cleanup(func() { auth.SetJWTSecret("") })

	token, err := auth.GenerateToken(3, "bob", "user")
	if err != nil {
		t.Fatal(err)
	}

	// 纯内存黑名单模式：注销后该 token 应视为匿名
	auth.SetBlacklistRedis(nil)
	auth.RevokeToken(context.Background(), token, 1<<20)

	ran := false
	var handler app.HandlerFunc = func(ctx context.Context, c *app.RequestContext) {
		ran = true
		uid, ok := UserIDFromContext(ctx)
		if ok || uid != 0 {
			t.Errorf("已注销 token 不应注入身份, got uid=%d", uid)
		}
		if _, exists := c.Get("user_id"); exists {
			t.Error("已注销 token 不应写入 c.Set(user_id)")
		}
		c.JSON(http.StatusOK, map[string]string{})
	}
	h := server.New(server.WithHostPorts("127.0.0.1:0"))
	h.POST("/share/text", append(OptionalIdentity(), handler)...)

	w := ut.PerformRequest(h.Engine, http.MethodPost, "/share/text", nil,
		ut.Header{"Authorization", "Bearer " + token})
	assert.DeepEqual(t, http.StatusOK, w.Result().StatusCode())
	if !ran {
		t.Fatal("匿名语义应放行 handler")
	}
}
