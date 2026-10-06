package bootstrap

import (
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/filescodebox/core/conf"
)

// database.auto_migrate 必须默认 true:env-only 形态(无 config.yaml,如
// openwrt/fnos 容器)该键无任何配置来源,取 Go 零值 false 会跳过 standalone
// 迁移步骤,全新安装缺 notifies 表 → /notifies/* 全 500(2026-10-07 真机事故)。
func TestSetDefaults_AutoMigrateDefaultTrue(t *testing.T) {
	v := viper.New()
	setDefaults(v)
	bindEnvironment(v)

	var cfg conf.AppConfiguration
	require.NoError(t, v.Unmarshal(&cfg))
	assert.True(t, cfg.Database.AutoMigrate,
		"auto_migrate 零配置时必须默认 true(否则 env-only 形态缺 notifies 表)")
}

// 显式关闭仍然生效(多副本 public/admin 副本依赖该关闭防迁移竞态)。
func TestSetDefaults_AutoMigrateExplicitFalse(t *testing.T) {
	v := viper.New()
	setDefaults(v)
	v.Set("database.auto_migrate", false)
	bindEnvironment(v)

	var cfg conf.AppConfiguration
	require.NoError(t, v.Unmarshal(&cfg))
	assert.False(t, cfg.Database.AutoMigrate)
}
