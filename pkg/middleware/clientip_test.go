package middleware

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestResolveClientIP 可信代理解析语义全场景（含伪造对照）
func TestResolveClientIP(t *testing.T) {
	require.NoError(t, SetTrustedProxies([]string{"10.0.0.0/8"}))
	t.Cleanup(func() { _ = SetTrustedProxies(nil) })

	t.Run("直连不可信+伪造XFF → 忽略代理头按直连", func(t *testing.T) {
		got := ResolveClientIP("203.0.113.9:55555", "1.2.3.4", "")
		assert.Equal(t, "203.0.113.9", got, "直连不在可信网段时伪造 XFF 必须被忽略")
	})
	t.Run("直连可信+XFF → 采信真实客户端", func(t *testing.T) {
		got := ResolveClientIP("10.0.0.2:443", "203.0.113.7, 10.0.0.2", "")
		assert.Equal(t, "203.0.113.7", got)
	})
	t.Run("直连可信+XFF全为可信代理 → 取最左端", func(t *testing.T) {
		got := ResolveClientIP("10.0.0.2:443", "10.0.0.3, 10.0.0.2", "")
		assert.Equal(t, "10.0.0.3", got)
	})
	t.Run("直连可信+无XFF+X-Real-IP → 采信 XRI", func(t *testing.T) {
		got := ResolveClientIP("10.0.0.2:443", "", "203.0.113.8")
		assert.Equal(t, "203.0.113.8", got)
	})
	t.Run("未配置可信网段直连部署 → 一律按直连", func(t *testing.T) {
		_ = SetTrustedProxies(nil)
		got := ResolveClientIP("203.0.113.9:55555", "1.2.3.4", "")
		assert.Equal(t, "203.0.113.9", got)
	})
}
