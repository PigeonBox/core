package middleware

import (
	"context"
	"strings"
	"testing"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/stretchr/testify/assert"
)

// TestRequestIDSanitizesClientTraceID 客户端自带 trace_id 必须过白名单
// （2026-10-06 审计：此前任意值原样回显进响应头并落入访问日志——超长串
// 可刷日志体积，控制字符可污染日志行）。
func TestRequestIDSanitizesClientTraceID(t *testing.T) {
	run := func(traceID string) (echoed, ctxVal string) {
		c := app.NewContext(1)
		if traceID != "" {
			c.Request.Header.Set(TraceIDHeader, traceID)
		}
		RequestID()(context.Background(), c)
		return string(c.Response.Header.Get(TraceIDHeader)), c.GetString("trace_id")
	}

	t.Run("合规值透传", func(t *testing.T) {
		echoed, _ := run("01HV2P3Q-finest-workstation")
		assert.Equal(t, "01HV2P3Q-finest-workstation", echoed, "合规 trace_id 应原样透传（链路串联语义）")
	})

	t.Run("超长值重新生成", func(t *testing.T) {
		echoed, _ := run(strings.Repeat("A", 800))
		assert.Len(t, echoed, 36, "超长 trace_id 必须替换为 UUID")
	})

	t.Run("特殊字符重新生成", func(t *testing.T) {
		for _, evil := range []string{"evil\ninjected", "a b", "x;y", "你好", "value$CRLF"} {
			echoed, _ := run(evil)
			assert.Len(t, echoed, 36, "非法字符 trace_id(%q) 必须替换为 UUID", evil)
		}
	})

	t.Run("缺失生成", func(t *testing.T) {
		echoed, ctxVal := run("")
		assert.Len(t, echoed, 36)
		assert.Equal(t, echoed, ctxVal)
	})
}
