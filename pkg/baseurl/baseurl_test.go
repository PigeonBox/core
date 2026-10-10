package baseurl

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

// ResolveBase 解析顺序回归:显式配置 > 请求来源(中间件注入 ctx) > 空串退相对路径。
// 禁止回退 server.host——监听地址(0.0.0.0)拼进分享链接对外不可达
// (2026-10-07 iStoreOS 真机事故:分享成功弹窗 http://0.0.0.0:12345 链接)。
func TestResolve(t *testing.T) {
	// 显式配置最优先
	assert.Equal(t, "https://s.example.com", Resolve(context.Background(), "https://s.example.com"))

	// base_url 未配置时取请求来源(bootstrap 中间件写入 ctx)。
	// 键必须是内置 string:hertz RequestContext.Value 只解析 string 键
	// (c.Set 存 map[string]any),自定义 key 类型会查不到。
	ctx := context.WithValue(context.Background(), PublicBaseCtxKey, "http://10.10.30.250:12345") //nolint:staticcheck // SA1029:hertz ctx 键约束见上
	assert.Equal(t, "http://10.10.30.250:12345", Resolve(ctx, ""))

	// 双缺省退空串(FullShareURL 退化为相对路径,前端可由 origin 补全)
	assert.Equal(t, "", Resolve(context.Background(), ""))
}
