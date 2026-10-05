package resp

import (
	"strings"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/stretchr/testify/assert"
)

// TestValidTraceID trace_id 白名单（与 pkg/middleware.RequestID 共用）
func TestValidTraceID(t *testing.T) {
	assert.True(t, ValidTraceID("01HV2P3Q-finest_workstation.42"))
	assert.True(t, ValidTraceID(strings.Repeat("a", 64)), "恰好 64 字符合法")
	assert.False(t, ValidTraceID(""))
	assert.False(t, ValidTraceID(strings.Repeat("a", 65)), "超 64 字符拒绝")
	assert.False(t, ValidTraceID("has space"))
	assert.False(t, ValidTraceID("evil\nline"))
	assert.False(t, ValidTraceID("semi;colon"))
	assert.False(t, ValidTraceID("中文"))
}

// TestGetOrGenTraceIDSanitizes envelope 里的 trace_id 同样必须过白名单
// （generated handler 大量直接 c.JSON，不经 resp helper，但 X-Trace-Id
// 响应头由 RequestID 中间件消毒；此处守住 resp 路径自己的回显）
func TestGetOrGenTraceIDSanitizes(t *testing.T) {
	c := app.NewContext(1)
	c.Request.Header.Set(traceIDHeader, strings.Repeat("A", 800))
	tid := getOrGenTraceID(c)
	assert.Len(t, tid, 36, "不合规 trace_id 必须重新生成")
	assert.Equal(t, tid, string(c.Response.Header.Get(traceIDHeader)))
}
