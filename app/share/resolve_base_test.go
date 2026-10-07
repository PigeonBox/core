package share

import (
	"context"
	"testing"

	"github.com/filescodebox/core/repo/db/model"
	"github.com/stretchr/testify/assert"
)

// ResolveBase 解析顺序回归:显式配置 > 请求来源(中间件注入 ctx) > 空串退相对路径。
// 禁止回退 server.host——监听地址(0.0.0.0)拼进分享链接对外不可达
// (2026-10-07 iStoreOS 真机事故:分享成功弹窗 http://0.0.0.0:12345 链接)。
func TestResolveBase(t *testing.T) {
	// 显式配置最优先
	assert.Equal(t, "https://s.example.com", ResolveBase(context.Background(), "https://s.example.com"))

	// base_url 未配置时取请求来源(bootstrap 中间件写入 ctx)。
	// 键必须是内置 string:hertz RequestContext.Value 只解析 string 键
	// (c.Set 存 map[string]any),自定义 key 类型会查不到。
	ctx := context.WithValue(context.Background(), PublicBaseCtxKey, "http://10.10.30.250:12345") //nolint:staticcheck // SA1029:hertz ctx 键约束见上
	assert.Equal(t, "http://10.10.30.250:12345", ResolveBase(ctx, ""))

	// 双缺省退空串(FullShareURL 退化为相对路径,前端可由 origin 补全)
	assert.Equal(t, "", ResolveBase(context.Background(), ""))
}

// 分享结果构造必须走 ResolveBase:base_url 未配置时按请求来源拼绝对链接,
// 无请求上下文(MCP/联邦等非 HTTP 调用方)退相对路径,绝不出现 0.0.0.0。
func TestModelToResp_FullShareURL(t *testing.T) {
	s := &Service{baseURL: ""}
	fc := &model.FileCode{Code: "VMnyAcrG"}

	// 键必须是内置 string:hertz RequestContext.Value 只解析 string 键
	// (c.Set 存 map[string]any),自定义 key 类型会查不到。
	ctx := context.WithValue(context.Background(), PublicBaseCtxKey, "http://10.10.30.250:12345") //nolint:staticcheck // SA1029:hertz ctx 键约束,同上
	resp := s.modelToResp(ctx, fc)
	assert.Equal(t, "http://10.10.30.250:12345/share/VMnyAcrG", resp.FullShareURL)

	resp2 := s.modelToResp(context.Background(), fc)
	assert.Equal(t, "/share/VMnyAcrG", resp2.FullShareURL)
	assert.NotContains(t, resp2.FullShareURL, "0.0.0.0")

	// 显式配置仍最优先
	s2 := &Service{baseURL: "https://s.example.com"}
	resp3 := s2.modelToResp(context.Background(), fc)
	assert.Equal(t, "https://s.example.com/share/VMnyAcrG", resp3.FullShareURL)
}
