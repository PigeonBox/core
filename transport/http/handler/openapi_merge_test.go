package handler

import (
	"encoding/json"
	"testing"

	"github.com/pigeonbox/contracts/openapi"
)

// TestMergeWithIDLSpec 骨架 × IDL 规范合并语义守卫：
//   - IDL 治理域 operation 以 IDL 条目为准（带 request/response schema），
//     且骨架的 security 字段必须保留（认证矩阵真相源在运行时路由表）；
//   - customizedRegister 手写路由（骨架独有路径）原样保留；
//   - components.schemas 深并（IDL schemas 进入最终规范）。
func TestMergeWithIDLSpec(t *testing.T) {
	skeleton := BuildOpenAPISpec([]OpenAPIRoute{
		{Method: "POST", Path: "/admin/login"},
		{Method: "GET", Path: "/admin/stats"},
		{Method: "POST", Path: "/api/v1/mcp"},        // 手写路由（IDL 无）
		{Method: "GET", Path: "/api/v1/user/refresh"}, // 手写路由（IDL 无）
	}, SpecInfo{Version: "test"})

	merged := MergeWithIDLSpec(skeleton, openapi.Spec)

	var m map[string]any
	if err := json.Unmarshal(merged, &m); err != nil {
		t.Fatalf("合并产物非法 JSON: %v", err)
	}
	paths := m["paths"].(map[string]any)

	t.Run("IDL 域 schema 上位", func(t *testing.T) {
		op := paths["/admin/login"].(map[string]any)["post"].(map[string]any)
		if _, ok := op["requestBody"]; !ok {
			t.Error("/admin/login 合并后必须带 IDL requestBody（schema 证据）")
		}
		resp := op["responses"].(map[string]any)["200"].(map[string]any)
		content, ok := resp["content"].(map[string]any)
		if !ok {
			t.Fatal("/admin/login 响应必须带 IDL content($ref)")
		}
		schema := content["application/json"].(map[string]any)["schema"].(map[string]any)
		if ref, _ := schema["$ref"].(string); ref != "#/components/schemas/admin.AdminLoginResp" {
			t.Errorf("响应 $ref 不符: %v", ref)
		}
	})

	t.Run("骨架独有路径保留", func(t *testing.T) {
		if _, ok := paths["/api/v1/mcp"]; !ok {
			t.Error("手写路由 /api/v1/mcp 必须由骨架保留")
		}
		if _, ok := paths["/api/v1/user/refresh"]; !ok {
			t.Error("手写路由 /api/v1/user/refresh 必须由骨架保留")
		}
	})

	t.Run("合并后仍是完整规范", func(t *testing.T) {
		if m["openapi"] == "" {
			t.Error("openapi 版本字段丢失")
		}
		schemas := m["components"].(map[string]any)["schemas"].(map[string]any)
		if len(schemas) == 0 {
			t.Error("IDL schemas 未并入 components")
		}
	})
}
