package handler

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestBuildOpenAPISpec(t *testing.T) {
	routes := []OpenAPIRoute{
		{Method: "GET", Path: "/health"},
		{Method: "POST", Path: "/admin/login"},
		{Method: "GET", Path: "/admin/files/:id"},
		{Method: "POST", Path: "/share/text/"},
		{Method: "GET", Path: "/share/select/"},
		{Method: "POST", Path: "/api/v1/share/multi-direct"},
		{Method: "DELETE", Path: "/api/v1/user/shares/:code/hard"},
	}
	spec := BuildOpenAPISpec(routes, SpecInfo{Version: "test", BaseURL: "http://x"})

	var m map[string]any
	if err := json.Unmarshal(spec, &m); err != nil {
		t.Fatalf("生成结果不是合法 JSON: %v", err)
	}
	if m["openapi"] != "3.0.3" {
		t.Fatalf("openapi 版本错误: %v", m["openapi"])
	}

	paths, _ := m["paths"].(map[string]any)
	if len(paths) != 7 { // 7 条路由各为独立路径（text/select 不合并）
		t.Fatalf("paths 数量错误: %d", len(paths))
	}

	// :param → {param} 转换
	item, ok := paths["/admin/files/{id}"].(map[string]any)
	if !ok {
		t.Fatalf("路径模板转换失败: %v", paths)
	}
	getOp := item["get"].(map[string]any)
	// 管理端 → adminAuth
	sec := getOp["security"].([]any)
	if len(sec) == 0 {
		t.Fatal("管理端点应带 adminAuth")
	}
	// 路径参数已声明
	if params, ok := getOp["parameters"].([]any); !ok || len(params) != 1 {
		t.Fatalf("路径参数缺失: %v", getOp["parameters"])
	}

	// 公开端点不带 security
	pub := paths["/share/select/"].(map[string]any)["get"].(map[string]any)
	if _, has := pub["security"]; has {
		t.Fatal("公开端点不应带 security")
	}

	// 双认证端点
	multi := paths["/api/v1/share/multi-direct"].(map[string]any)["post"].(map[string]any)
	if sec := multi["security"].([]any); len(sec) != 2 {
		t.Fatalf("多文件端点应 userAuth/apiKeyAuth 任一: %v", sec)
	}

	// 输出稳定（diff/缓存友好）
	if string(BuildOpenAPISpec(routes, SpecInfo{Version: "test"})) != string(BuildOpenAPISpec(routes, SpecInfo{Version: "test"})) {
		t.Fatal("生成结果不稳定")
	}
}

func TestSecurityForPriorities(t *testing.T) {
	// 登录端点公开，尽管前缀是 /admin、/user
	if sec, _ := securityFor("POST", "/admin/login"); sec != nil {
		t.Fatal("/admin/login 应公开")
	}
	if sec, _ := securityFor("GET", "/api/v1/user/oidc/login"); sec != nil {
		t.Fatal("OIDC 登录入口应公开")
	}
	// 其余 admin 全部受保护
	if sec, _ := securityFor("GET", "/admin/files"); sec == nil {
		t.Fatal("/admin/files 应受保护")
	}
	// method 与 path 分离匹配：path 前缀不再被 method 干扰
	if sec, _ := securityFor("PUT", "/admin/config/user"); sec == nil {
		t.Fatal("/admin/config/user 应受保护")
	}
	if !strings.Contains(operationID("GET", "/admin/files/:id"), "By") {
		t.Fatal("operationID 未处理路径参数")
	}
}
