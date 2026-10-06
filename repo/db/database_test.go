package db

import (
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// autoMigrate 登记表清单回归:notify 表此前只靠 bootstrap standalone 步骤迁移,
// 而该步骤被 database.auto_migrate 门控——env-only 形态(无 yaml,该键取零值
// false)下全新安装缺表,/notifies/* 全 500(2026-10-07 真机事故)。
// 登记进本清单后,autoMigrate 路径(默认初始化路径)必须建出该表。
func TestAutoMigrateCreatesNotifyTable(t *testing.T) {
	gormDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)

	old := DB
	DB = gormDB
	t.Cleanup(func() { DB = old })

	require.NoError(t, autoMigrate())
	assert.True(t, DB.Migrator().HasTable("notifies"),
		"notifies 表必须在 autoMigrate 清单内(漏登记=env-only 全新安装 500)")
}
