package dao

import (
	"context"
	"testing"

	"github.com/glebarez/sqlite"
	"github.com/pigeonbox/core/repo/db"
	"github.com/pigeonbox/core/repo/db/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

// newInjectTestDB 开一个隔离的 :memory: 库并迁移 FileCode 表。
// 注意：glebarez/sqlite 的 :memory: 每条连接是独立库，钉死单连接
// （同 filecode_governance_test.go 的 newGovernanceTestDB 处理方式）。
func newInjectTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	g, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, g.AutoMigrate(&model.FileCode{}))
	sqlDB, err := g.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	return g
}

// TestInjectedRepositoryWorks 示范注入消费：全局 DB 为 nil 时，
// NewFileCodeRepository(gormDB) 注入实例仍能完整走通 Create + GetByCode，
// 证明 repository 不触碰任何全局 DB 状态（去全局化目标）。
func TestInjectedRepositoryWorks(t *testing.T) {
	// 显式清空全局实例，确定性复现"未初始化全局 DB"的进程环境
	// （同包其他用例经 cleanup 恢复 nil，这里再钉一次不依赖执行顺序）。
	db.SetDatabaseInstance(nil)
	t.Cleanup(func() { db.SetDatabaseInstance(nil) })
	require.Nil(t, db.GetDB(), "前置条件：全局 DB 必须为 nil")

	g := newInjectTestDB(t)
	repo := NewFileCodeRepository(g)
	ctx := context.Background()

	require.NoError(t, repo.Create(ctx, &model.FileCode{Code: "INJECT01", Status: model.StatusNormal}))

	got, err := repo.GetByCode(ctx, "INJECT01")
	require.NoError(t, err)
	assert.Equal(t, "INJECT01", got.Code)
	assert.Equal(t, model.StatusNormal, got.Status)
}

// TestInjectedRepositoriesAreIsolated 注入隔离性：两个独立 :memory: 实例
// 各自注入的 repository 互不可见对方数据（无参构造走全局，本用例全程
// 全局为 nil——若哪个 repository 暗中回退全局会直接 panic 暴露）。
func TestInjectedRepositoriesAreIsolated(t *testing.T) {
	db.SetDatabaseInstance(nil)
	t.Cleanup(func() { db.SetDatabaseInstance(nil) })

	g1, g2 := newInjectTestDB(t), newInjectTestDB(t)
	repo1, repo2 := NewFileCodeRepository(g1), NewFileCodeRepository(g2)
	ctx := context.Background()

	require.NoError(t, repo1.Create(ctx, &model.FileCode{Code: "ONLYIN01", Status: model.StatusNormal}))

	_, err := repo2.GetByCode(ctx, "ONLYIN01")
	assert.ErrorIs(t, err, gorm.ErrRecordNotFound, "实例 2 不得读到实例 1 的数据")

	// 无参构造兼容性：全局置为 g2 后，历史无参调用与新注入路径读到同一份库
	db.SetDatabaseInstance(g2)
	require.NoError(t, NewFileCodeRepository().Create(ctx, &model.FileCode{Code: "GLOBAL02", Status: model.StatusNormal}))
	got, err := repo2.GetByCode(ctx, "GLOBAL02")
	require.NoError(t, err)
	assert.Equal(t, "GLOBAL02", got.Code)
}
