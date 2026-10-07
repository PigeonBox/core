package bootstrap

import (
	"testing"

	"github.com/pigeonbox/core/conf"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestApplyDeploymentConstraints_RequiresRedis 多副本模式必须显式配置 Redis
// （多副本硬约束运行时化）：缺失即 fail-fast，防静默状态分裂。
func TestApplyDeploymentConstraints_RequiresRedis(t *testing.T) {
	newCfg := func(mode string) *conf.AppConfiguration {
		cfg := &conf.AppConfiguration{}
		cfg.Deployment.Mode = mode
		cfg.Database.Driver = "mysql"
		return cfg
	}

	t.Run("public 无 redis fail-fast", func(t *testing.T) {
		err := applyDeploymentConstraints(newCfg(conf.DeployModePublic))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "redis.host")
	})

	t.Run("admin 无 redis fail-fast", func(t *testing.T) {
		err := applyDeploymentConstraints(newCfg(conf.DeployModeAdmin))
		require.Error(t, err)
		assert.Contains(t, err.Error(), "redis.host")
	})

	t.Run("public 配置 redis 通过", func(t *testing.T) {
		cfg := newCfg(conf.DeployModePublic)
		cfg.Redis.Host = "redis"
		require.NoError(t, applyDeploymentConstraints(cfg))
	})

	t.Run("standalone 无 redis 放行（单机内存模式）", func(t *testing.T) {
		cfg := newCfg(conf.DeployModeStandalone)
		cfg.Database.Driver = "sqlite"
		require.NoError(t, applyDeploymentConstraints(cfg))
	})
}
