package dao

import (
	"context"
	"testing"
	"time"

	"github.com/filescodebox/core/repo/db"
	"github.com/filescodebox/core/repo/db/model"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newGovernanceTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	g, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, g.AutoMigrate(&model.FileCode{}))
	db.SetDatabaseInstance(g)
	t.Cleanup(func() { db.SetDatabaseInstance(nil) })
	return g
}

func TestUpdateStatusByIDs(t *testing.T) {
	newGovernanceTestDB(t)
	repo := NewFileCodeRepository()
	ctx := context.Background()

	fc1 := &model.FileCode{Code: "AAA11111"}
	fc2 := &model.FileCode{Code: "BBB22222"}
	require.NoError(t, repo.Create(ctx, fc1))
	require.NoError(t, repo.Create(ctx, fc2))

	t.Run("非法状态拒绝", func(t *testing.T) {
		_, err := repo.UpdateStatusByIDs(ctx, []uint{fc1.ID}, "hacked")
		assert.Error(t, err)
	})
	t.Run("批量禁用后可恢复", func(t *testing.T) {
		n, err := repo.UpdateStatusByIDs(ctx, []uint{fc1.ID, fc2.ID}, model.StatusBlocked)
		require.NoError(t, err)
		assert.Equal(t, int64(2), n)

		got, err := repo.GetByCode(ctx, "AAA11111")
		require.NoError(t, err)
		assert.Equal(t, model.StatusBlocked, got.Status)

		n, err = repo.UpdateStatusByIDs(ctx, []uint{fc1.ID}, model.StatusNormal)
		require.NoError(t, err)
		assert.Equal(t, int64(1), n)
	})
	t.Run("新记录默认normal", func(t *testing.T) {
		require.NoError(t, repo.Create(ctx, &model.FileCode{Code: "CCC33333"}))
		got, err := repo.GetByCode(ctx, "CCC33333")
		require.NoError(t, err)
		// AutoMigrate 默认值：Create 未显式赋 Status 时落库为 normal
		assert.Equal(t, model.StatusNormal, got.Status)
	})
}

func TestListWithFilter(t *testing.T) {
	newGovernanceTestDB(t)
	repo := NewFileCodeRepository()
	ctx := context.Background()

	uid := uint(7)
	big := int64(1024 * 1024)
	now := time.Now()
	fixtures := []*model.FileCode{
		{Code: "FILEAAAA", UserID: &uid, UploadType: "authenticated", OwnerIP: "1.2.3.4", Size: 100, Status: model.StatusNormal},
		{Code: "FILEBBBB", UploadType: "anonymous", OwnerIP: "5.6.7.8", Size: big, Status: model.StatusBlocked},
		{Code: "FILECCCC", UploadType: "anonymous", OwnerIP: "1.2.3.4", Size: 500, Status: model.StatusNormal},
	}
	for _, f := range fixtures {
		require.NoError(t, repo.Create(ctx, f))
	}
	_ = now

	t.Run("按OwnerIP过滤", func(t *testing.T) {
		files, total, err := repo.ListWithFilter(ctx, model.FileCodeQuery{OwnerIP: "1.2.3.4"})
		require.NoError(t, err)
		assert.Equal(t, int64(2), total)
		assert.Len(t, files, 2)
	})
	t.Run("按状态过滤", func(t *testing.T) {
		files, total, err := repo.ListWithFilter(ctx, model.FileCodeQuery{Status: model.StatusBlocked})
		require.NoError(t, err)
		assert.Equal(t, int64(1), total)
		assert.Equal(t, "FILEBBBB", files[0].Code)
	})
	t.Run("按用户过滤", func(t *testing.T) {
		_, total, err := repo.ListWithFilter(ctx, model.FileCodeQuery{UserID: &uid})
		require.NoError(t, err)
		assert.Equal(t, int64(1), total)
	})
	t.Run("按类型+大小区间", func(t *testing.T) {
		minS, maxS := int64(400), int64(2*1024*1024)
		_, total, err := repo.ListWithFilter(ctx, model.FileCodeQuery{
			UploadType: "anonymous", MinSize: &minS, MaxSize: &maxS,
		})
		require.NoError(t, err)
		assert.Equal(t, int64(2), total) // FILEBBBB(big) + FILECCCC(500)
	})
	t.Run("keyword模糊", func(t *testing.T) {
		_, total, err := repo.ListWithFilter(ctx, model.FileCodeQuery{Keyword: "AAAA"})
		require.NoError(t, err)
		assert.Equal(t, int64(1), total)
	})
	t.Run("组合条件", func(t *testing.T) {
		_, total, err := repo.ListWithFilter(ctx, model.FileCodeQuery{
			OwnerIP: "1.2.3.4", Status: model.StatusNormal,
		})
		require.NoError(t, err)
		assert.Equal(t, int64(2), total)
	})
}
