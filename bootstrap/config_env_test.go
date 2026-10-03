package bootstrap

import (
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/filescodebox/core/conf"
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
