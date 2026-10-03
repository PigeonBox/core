package admin

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/filescodebox/core/repo/db"
	"github.com/filescodebox/core/repo/db/model"
	"github.com/filescodebox/core/storage"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/gorm"
)

func newJanitorTestEnv(t *testing.T) (string, *storage.StorageService) {
	t.Helper()
	root := t.TempDir()
	g, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, g.AutoMigrate(
		&model.FileCode{}, &model.UploadChunk{},
		&model.TransferLog{}, &model.AdminOperationLog{},
	))
	db.SetDatabaseInstance(g)
	t.Cleanup(func() { db.SetDatabaseInstance(nil) })

	svc, err := storage.NewStorageServiceE(&storage.StorageConfig{
		Type:     storage.StorageTypeLocal,
		DataPath: root,
	})
	require.NoError(t, err)
	return root, svc
}

func TestReconcileOrphans(t *testing.T) {
	root, svc := newJanitorTestEnv(t)
	ctx := context.Background()

	// 被 DB 引用的文件（保留）
	keepRel := "uploads/2026/10/03/keep-uuid.txt"
	keepAbs := filepath.Join(root, filepath.FromSlash(keepRel))
	require.NoError(t, os.MkdirAll(filepath.Dir(keepAbs), 0o755))
	require.NoError(t, os.WriteFile(keepAbs, []byte("keep"), 0o644))
	require.NoError(t, db.GetDB().Create(&model.FileCode{
		Code: "KEEPAAAA", FilePath: "uploads/2026/10/03", UUIDFileName: "keep-uuid.txt",
	}).Error)

	// 无引用的孤儿文件（删除）
	orphanAbs := filepath.Join(root, "uploads/2026/10/03/orphan-uuid.txt")
	require.NoError(t, os.WriteFile(orphanAbs, []byte("orphan"), 0o644))

	// 软删记录引用的文件（保留，可恢复）
	deletedRel := "uploads/2026/10/03/softdel-uuid.txt"
	softAbs := filepath.Join(root, filepath.FromSlash(deletedRel))
	require.NoError(t, os.WriteFile(softAbs, []byte("soft"), 0o644))
	require.NoError(t, db.GetDB().Create(&model.FileCode{
		Code: "SOFTAAAA", FilePath: "uploads/2026/10/03", UUIDFileName: "softdel-uuid.txt",
	}).Error)
	require.NoError(t, db.GetDB().Delete(&model.FileCode{}, "code = ?", "SOFTAAAA").Error)

	// 活跃分片目录（保留）与孤儿分片目录（删除）
	activeChunk := filepath.Join(root, "chunks", "active-upload")
	require.NoError(t, os.MkdirAll(activeChunk, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(activeChunk, "0.part"), []byte("c"), 0o644))
	require.NoError(t, db.GetDB().Create(&model.UploadChunk{
		UploadID: "active-upload", ChunkIndex: -1,
	}).Error)
	orphanChunk := filepath.Join(root, "chunks", "orphan-upload")
	require.NoError(t, os.MkdirAll(orphanChunk, 0o755))

	j := NewJanitor(svc, 90)
	scanned, removed, err := j.ReconcileOrphans(ctx)
	require.NoError(t, err)
	assert.GreaterOrEqual(t, scanned, 2)
	assert.Equal(t, 2, removed) // 孤儿文件 + 孤儿分片目录

	_, err = os.Stat(keepAbs)
	assert.NoError(t, err, "被引用文件应保留")
	_, err = os.Stat(orphanAbs)
	assert.True(t, os.IsNotExist(err), "孤儿文件应删除")
	_, err = os.Stat(softAbs)
	assert.NoError(t, err, "软删记录引用的文件应保留")
	_, err = os.Stat(activeChunk)
	assert.NoError(t, err, "活跃分片目录应保留")
	_, err = os.Stat(orphanChunk)
	assert.True(t, os.IsNotExist(err), "孤儿分片目录应删除")
}

func TestCleanupLogs(t *testing.T) {
	_, svc := newJanitorTestEnv(t)
	ctx := context.Background()

	old := time.Now().AddDate(0, 0, -30)
	// go1.26 不支持提升字段（gorm.Model.CreatedAt）结构体字面量，先建后回填时间
	lg1 := &model.TransferLog{Operation: "upload"}
	require.NoError(t, db.GetDB().Create(lg1).Error)
	require.NoError(t, db.GetDB().Model(lg1).UpdateColumn("created_at", old).Error)
	lg2 := &model.AdminOperationLog{Action: "x"}
	require.NoError(t, db.GetDB().Create(lg2).Error)
	require.NoError(t, db.GetDB().Model(lg2).UpdateColumn("created_at", old).Error)
	lg3 := &model.TransferLog{Operation: "upload"}
	require.NoError(t, db.GetDB().Create(lg3).Error)

	j := NewJanitor(svc, 7)
	n, err := j.CleanupLogs(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(2), n)

	var cnt int64
	require.NoError(t, db.GetDB().Model(&model.TransferLog{}).Count(&cnt).Error)
	assert.Equal(t, int64(1), cnt, "只删超过保留期的")

	// 0 = 永久保留
	j0 := NewJanitor(svc, 0)
	n, err = j0.CleanupLogs(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(0), n)
}
