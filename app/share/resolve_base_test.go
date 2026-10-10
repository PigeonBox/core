package share

import (
	"context"
	"testing"

	"github.com/pigeonbox/core/pkg/baseurl"
	"github.com/pigeonbox/core/repo/db/model"
	"github.com/stretchr/testify/assert"
)

// 分享结果构造必须走 baseurl.Resolve:base_url 未配置时按请求来源拼绝对链接,
// 无请求上下文(MCP/联邦等非 HTTP 调用方)退相对路径,绝不出现 0.0.0.0。
func TestModelToResp_FullShareURL(t *testing.T) {
	s := &Service{baseURL: ""}
	fc := &model.FileCode{Code: "VMnyAcrG"}

	// 键必须是内置 string:hertz RequestContext.Value 只解析 string 键
	// (c.Set 存 map[string]any),自定义 key 类型会查不到。
	ctx := context.WithValue(context.Background(), baseurl.PublicBaseCtxKey, "http://10.10.30.250:12345") //nolint:staticcheck // SA1029:hertz ctx 键约束,同上
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
