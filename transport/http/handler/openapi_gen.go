// 运行时 OpenAPI 规范生成。
//
// 背景：openapi.json 曾依赖手工维护的快照文件（拆分后与路由漂移、容器内恒 404，
// 前端 /api-docs Swagger 页拉不到 spec）。这里改为在路由注册完成后从 hertz
// 运行时路由表生成「契约级骨架规范」——与实际注册路由零漂移，覆盖端点/方法/
// 认证方式/路径参数。
//
// 边界（有意为之）：骨架不内联请求/响应 schema，字段级真相源在 contracts 仓
// thrift IDL；本规范用于端点发现、认证方式说明与 Swagger UI 可用性。
package handler

import (
	"encoding/json"
	"sort"
	"strings"
)

// OpenAPIRoute 参与生成的最小路由信息（与 hertz 路由表类型解耦）
type OpenAPIRoute struct {
	Method string
	Path   string
}

// SpecInfo 规范元信息
type SpecInfo struct {
	Version string // API 版本号（bootstrap 注入）
	BaseURL string // 空则省略 servers
}

// openapiSpecBytes 生成的规范（bootstrap 在 customizedRegister 后注入）
var openapiSpecBytes []byte

// SetOpenAPISpecBytes 注入生成的规范
func SetOpenAPISpecBytes(b []byte) { openapiSpecBytes = b }

// BuildOpenAPISpec 从路由表生成 OpenAPI 3.0 骨架规范
func BuildOpenAPISpec(routes []OpenAPIRoute, info SpecInfo) []byte {
	paths := map[string]any{}
	tagSet := map[string]bool{}
	for _, r := range routes {
		if r.Path == "" || r.Path[0] != '/' {
			continue
		}
		specPath, pathParams := openapiPathTemplate(r.Path)
		op := map[string]any{
			"summary":     strings.ToUpper(r.Method) + " " + r.Path,
			"operationId": operationID(r.Method, r.Path),
			"tags":        []string{tagFor(r.Path)},
			"responses": map[string]any{
				"200": map[string]any{
					"description": "成功（业务码见响应体 code 字段，code=0 表示成功）",
				},
			},
		}
		if len(pathParams) > 0 {
			op["parameters"] = pathParams
		}
		if sec, note := securityFor(r.Method, r.Path); len(sec) > 0 {
			op["security"] = sec
			if note != "" {
				op["description"] = note
			}
		} else if note != "" {
			op["description"] = note
		}
		tagSet[tagFor(r.Path)] = true
		item, _ := paths[specPath].(map[string]any)
		if item == nil {
			item = map[string]any{}
			paths[specPath] = item
		}
		item[strings.ToLower(r.Method)] = op
	}

	tags := make([]string, 0, len(tagSet))
	for t := range tagSet {
		tags = append(tags, t)
	}
	sort.Strings(tags)
	tagItems := make([]map[string]string, 0, len(tags))
	for _, t := range tags {
		tagItems = append(tagItems, map[string]string{"name": t})
	}

	spec := map[string]any{
		"openapi": "3.0.3",
		"info": map[string]any{
			"title":   "PigeonBox API",
			"version": info.Version,
			"description": "契约级骨架规范：由运行时路由表自动生成，与实际注册路由零漂移。" +
				"请求/响应字段 schema 的真相源在 contracts 仓 thrift IDL；本规范用于端点发现与认证方式说明。",
		},
		"tags":  tagItems,
		"paths": sortedPaths(paths),
		"components": map[string]any{
			"securitySchemes": map[string]any{
				"adminAuth": map[string]any{
					"type": "http", "scheme": "bearer", "bearerFormat": "JWT",
					"description": "管理员 JWT（POST /admin/login 获取）",
				},
				"userAuth": map[string]any{
					"type": "http", "scheme": "bearer", "bearerFormat": "JWT",
					"description": "用户 JWT（POST /user/login 获取）",
				},
				"apiKeyAuth": map[string]any{
					"type": "apiKey", "in": "header", "name": "Authorization",
					"description": "用户 API Key：Authorization: Bearer pb_sk_...（/api/v1 双认证端点可用）",
				},
			},
		},
	}
	if info.BaseURL != "" {
		spec["servers"] = []map[string]string{{"url": info.BaseURL}}
	}
	b, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return []byte("{}")
	}
	return b
}

// sortedPaths 输出按路径排序的 paths（生成结果稳定，利于 diff/缓存）
func sortedPaths(paths map[string]any) map[string]any {
	keys := make([]string, 0, len(paths))
	for k := range paths {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make(map[string]any, len(paths))
	for _, k := range keys {
		out[k] = paths[k]
	}
	return out
}

// openapiPathTemplate 把 hertz 路由 ":param" 转成 OpenAPI "{param}" 并返回路径参数定义
func openapiPathTemplate(p string) (string, []map[string]any) {
	segs := strings.Split(p, "/")
	var params []map[string]any
	for i, seg := range segs {
		if len(seg) > 1 && seg[0] == ':' {
			name := seg[1:]
			segs[i] = "{" + name + "}"
			params = append(params, map[string]any{
				"name": name, "in": "path", "required": true,
				"schema": map[string]string{"type": "string"},
			})
		}
	}
	return strings.Join(segs, "/"), params
}

// operationID 生成稳定的 operationId（method+path 归一化）
func operationID(method, path string) string {
	var b strings.Builder
	b.WriteString(strings.ToLower(method))
	for _, seg := range strings.Split(path, "/") {
		if seg == "" {
			continue
		}
		if seg[0] == ':' {
			b.WriteString("By")
			seg = seg[1:]
		}
		b.WriteString(strings.ToUpper(seg[:1]) + seg[1:])
	}
	return b.String()
}

// tagFor 按路径前缀归入业务 tag
func tagFor(path string) string {
	switch {
	case strings.HasPrefix(path, "/admin/"):
		rest := strings.TrimPrefix(path, "/admin/")
		if i := strings.IndexByte(rest, '/'); i > 0 {
			return "admin/" + rest[:i]
		}
		return "admin"
	case strings.HasPrefix(path, "/admin"):
		return "admin"
	case strings.HasPrefix(path, "/api/v1/user/shares"):
		return "share"
	case strings.HasPrefix(path, "/api/v1/user/requests"), strings.HasPrefix(path, "/request/"):
		return "request"
	case strings.HasPrefix(path, "/api/v1/user/oidc"):
		return "auth"
	case strings.HasPrefix(path, "/api/v1/user"):
		return "user"
	case strings.HasPrefix(path, "/api/v1/presign"):
		return "presign"
	case strings.HasPrefix(path, "/api/v1/share"):
		return "share"
	case strings.HasPrefix(path, "/api/v1/mcp"):
		return "mcp"
	case strings.HasPrefix(path, "/share"):
		return "share"
	case strings.HasPrefix(path, "/anonymous"):
		return "anonymous"
	case strings.HasPrefix(path, "/chunk"):
		return "chunk"
	case strings.HasPrefix(path, "/user"):
		return "user"
	case strings.HasPrefix(path, "/notifies"):
		return "notify"
	case strings.HasPrefix(path, "/preview"):
		return "preview"
	case strings.HasPrefix(path, "/qrcode"):
		return "qrcode"
	case strings.HasPrefix(path, "/setup"):
		return "setup"
	case strings.HasPrefix(path, "/health"), strings.HasPrefix(path, "/live"),
		strings.HasPrefix(path, "/ping"), strings.HasPrefix(path, "/ready"),
		strings.HasPrefix(path, "/version"), strings.HasPrefix(path, "/readyz"):
		return "health"
	default:
		return "common"
	}
}

// securityFor (method, path) → 认证方式。返回 (security, description 备注)。
// 规则按「最具体优先」排列；未命中视为公开端点。
func securityFor(method, path string) ([]map[string]any, string) {
	anyOfUserOrKey := []map[string]any{{"userAuth": []string{}}, {"apiKeyAuth": []string{}}}
	userOnly := []map[string]any{{"userAuth": []string{}}}
	switch {
	// 公开
	case path == "/admin/login", path == "/user/login", path == "/user/register":
		return nil, ""
	// MCP（2026-10-05 审计 P3：AdminMiddleware 保护但此前被标为公开）
	case strings.HasPrefix(path, "/api/v1/mcp"):
		return []map[string]any{{"adminAuth": []string{}}}, ""
	case strings.HasPrefix(path, "/api/v1/user/oidc/"):
		return nil, ""
	case strings.HasPrefix(path, "/request/"), strings.HasPrefix(path, "/api/v1/request/"):
		return nil, "寄件码链路：凭投递令牌访问（令牌由分享者下发）"
	// 管理端（AdminMiddleware）
	case strings.HasPrefix(path, "/admin"):
		return []map[string]any{{"adminAuth": []string{}}}, ""
	// 用户/JWT
	case strings.HasPrefix(path, "/user/api-keys"), strings.HasPrefix(path, "/user/info"),
		strings.HasPrefix(path, "/user/stats"), strings.HasPrefix(path, "/user/files"),
		strings.HasPrefix(path, "/user/profile"), strings.HasPrefix(path, "/user/change-password"),
		strings.HasPrefix(path, "/api/v1/user/"):
		return userOnly, ""
	// 双认证（JWT 或 API Key）
	case strings.HasPrefix(path, "/api/v1/presign"), strings.HasPrefix(path, "/chunk"):
		return anyOfUserOrKey, "需要用户身份：JWT 或 API Key 任一"
	case strings.HasPrefix(path, "/api/v1/share/multi"):
		return anyOfUserOrKey, "身份可选：匿名可创建（受上传闸门约束），登录用户计入自己的分享管理"
	default:
		return nil, ""
	}
}

// MergeWithIDLSpec 骨架规范 × contracts IDL 规范（openapi.Spec）合并。
//
// 设计（2026-10-06 IDL/hz 治理链路）：
//   - IDL 治理域：paths 下的 operation 以 IDL 条目为准（带完整 request/response
//     schema），但保留骨架独有的 security/tags 字段（认证矩阵真相源在运行时）；
//   - customizedRegister 手写路由：骨架独有路径/方法原样保留（骨架级、无 schema）；
//   - components：骨架的 securitySchemes 与 IDL 的 schemas 深并，键冲突 IDL 优先。
//
// 任一侧解析失败均回退骨架（可用性优先）。
func MergeWithIDLSpec(skeleton, idl []byte) []byte {
	var skel, id map[string]any
	if json.Unmarshal(skeleton, &skel) != nil || json.Unmarshal(idl, &id) != nil {
		return skeleton
	}

	skelPaths, _ := skel["paths"].(map[string]any)
	idPaths, _ := id["paths"].(map[string]any)
	for p, ops := range idPaths {
		idOps, ok := ops.(map[string]any)
		if !ok {
			continue
		}
		skelOps, ok := skelPaths[p].(map[string]any)
		if !ok {
			// 骨架没有该路径（罕见：IDL 有、运行时未注册），整段并入
			skelPaths[p] = idOps
			continue
		}
		for m, op := range idOps {
			idOp, ok := op.(map[string]any)
			if !ok {
				continue
			}
			skelOp, ok := skelOps[m].(map[string]any)
			if !ok {
				skelOps[m] = idOp
				continue
			}
			// 同路径同方法：骨架字段打底，IDL 字段覆盖（schema 赢，security/tags 留）
			merged := map[string]any{}
			for k, v := range skelOp {
				merged[k] = v
			}
			for k, v := range idOp {
				merged[k] = v
			}
			if _, has := skelOp["security"]; has {
				merged["security"] = skelOp["security"]
			}
			skelOps[m] = merged
		}
	}

	// components 深并：securitySchemes 留骨架，schemas IDL 优先
	skelComp, _ := skel["components"].(map[string]any)
	idComp, _ := id["components"].(map[string]any)
	if skelComp == nil {
		skelComp = map[string]any{}
		skel["components"] = skelComp
	}
	if idSchemas, ok := idComp["schemas"].(map[string]any); ok {
		skelSchemas, ok := skelComp["schemas"].(map[string]any)
		if !ok {
			skelSchemas = map[string]any{}
			skelComp["schemas"] = skelSchemas
		}
		for k, v := range idSchemas {
			skelSchemas[k] = v
		}
	}
	if _, ok := skelComp["securitySchemes"]; !ok {
		if ss, ok := idComp["securitySchemes"]; ok {
			skelComp["securitySchemes"] = ss
		}
	}

	out, err := json.Marshal(skel)
	if err != nil {
		return skeleton
	}
	return out
}
