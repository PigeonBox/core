package bootstrap

import (
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/pigeonbox/core/conf"
)

// 波次3：security.api_token.* 环境变量绑定（显式映射表，非 AutomaticEnv）。
func TestBindEnvironment_APIToken(t *testing.T) {
	t.Setenv("FCB_API_TOKEN_PER_KEY_QPS", "1")
	t.Setenv("FCB_API_TOKEN_PER_KEY_BURST", "3")

	v := viper.New()
	v.SetDefault("security.api_token.enabled", true)
	v.SetDefault("security.api_token.per_key_qps", 20)
	v.SetDefault("security.api_token.per_key_burst", 40)
	bindEnvironment(v)

	var cfg conf.AppConfiguration
	require.NoError(t, v.Unmarshal(&cfg))
	assert.Equal(t, 1, cfg.Security.APIToken.PerKeyQPS, "env 必须覆盖 SetDefault")
	assert.Equal(t, 3, cfg.Security.APIToken.PerKeyBurst)
	assert.True(t, cfg.Security.APIToken.Enabled)
}

// TestBindEnvironment_EnableHSTS_TriState HSTS 三态绑定（2026-10-08 加固）：
// env 字符串必须能正确解到 *bool（viper WeaklyTypedInput），未设置时保持 nil
// 交由 bootstrap 按 IsProduction 裁决。
func TestBindEnvironment_EnableHSTS_TriState(t *testing.T) {
	t.Run("env_true", func(t *testing.T) {
		t.Setenv("FCB_ENABLE_HSTS", "true")
		v := viper.New()
		bindEnvironment(v)
		var cfg conf.AppConfiguration
		require.NoError(t, v.Unmarshal(&cfg))
		require.NotNil(t, cfg.Security.CORS.EnableHSTS)
		assert.True(t, *cfg.Security.CORS.EnableHSTS)
	})
	t.Run("env_false", func(t *testing.T) {
		t.Setenv("FCB_ENABLE_HSTS", "false")
		v := viper.New()
		bindEnvironment(v)
		var cfg conf.AppConfiguration
		require.NoError(t, v.Unmarshal(&cfg))
		require.NotNil(t, cfg.Security.CORS.EnableHSTS)
		assert.False(t, *cfg.Security.CORS.EnableHSTS, "显式 false 必须能关掉生产默认")
	})
	t.Run("unset_nil", func(t *testing.T) {
		v := viper.New()
		bindEnvironment(v)
		var cfg conf.AppConfiguration
		require.NoError(t, v.Unmarshal(&cfg))
		assert.Nil(t, cfg.Security.CORS.EnableHSTS, "未配置必须保持 nil（三态语义的前提）")
	})
}
