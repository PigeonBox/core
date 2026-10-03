package admin

import (
	"context"
	"io"
	"mime/multipart"
	"sync"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/filescodebox/core/repo/db"
	"github.com/filescodebox/core/repo/db/model"
	"github.com/filescodebox/core/storage"
	"gorm.io/gorm"
)

// mockStorage 记录被删除的物理文件路径（实现完整 StorageInterface）
type mockStorage struct {
	mu      sync.Mutex
	deleted []string
}

func (m *mockStorage) SaveFile(_ context.Context, _ *multipart.FileHeader, _ string) (*storage.FileOperationResult, error) {
	return &storage.FileOperationResult{}, nil
}
func (m *mockStorage) DeleteFile(_ context.Context, path string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deleted = append(m.deleted, path)
	return nil
}
func (m *mockStorage) GetFile(_ context.Context, _ string) ([]byte, error) { return nil, nil }
func (m *mockStorage) FileExists(_ context.Context, _ string) bool         { return false }
func (m *mockStorage) GetFileSize(_ context.Context, _ string) (int64, error) {
	return 0, nil
}
func (m *mockStorage) GetFileURL(_ context.Context, _ string) (string, error) {
	return "", nil
}
func (m *mockStorage) GetFileReader(_ context.Context, _ string) (io.ReadCloser, int64, error) {
	return nil, 0, nil
}
func (m *mockStorage) SaveChunk(_ context.Context, _ string, _ int, _ []byte) error { return nil }
func (m *mockStorage) MergeChunks(_ context.Context, _ string, _ int, _ string) error {
	return nil
}
func (m *mockStorage) CleanChunks(_ context.Context, _ string) error { return nil }

func newAdminTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	g, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, g.AutoMigrate(&model.FileCode{}, &model.SystemConfigRecord{}))
	db.SetDatabaseInstance(g)
	t.Cleanup(func() { db.SetDatabaseInstance(nil) })
	return g
}

// TestCleanExpiredFiles_DeletesPhysicalFile 验证过期清理同时删 DB 记录和物理文件
func TestCleanExpiredFiles_DeletesPhysicalFile(t *testing.T) {
	newAdminTestDB(t)
	svc := NewService()
	st := &mockStorage{}
	svc.SetStorage(st)

	ctx := context.Background()
	// 过期文件（时间过期）
	past := time.Now().Add(-time.Hour)
	require.NoError(t, svc.fileCodeRepo.Create(ctx, &model.FileCode{
		Code: "EXP1", FilePath: "data/2024", UUIDFileName: "abc.txt",
		Size: 100, ExpiredAt: &past, ExpiredCount: -1,
	}))
	// 未过期文件（不应被删）
	require.NoError(t, svc.fileCodeRepo.Create(ctx, &model.FileCode{
		Code: "OK1", FilePath: "data/2024", UUIDFileName: "ok.txt",
		Size: 50, ExpiredCount: -1,
	}))

	deleted, freed, err := svc.CleanExpiredFiles(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)
	assert.Equal(t, int64(100), freed)

	// 物理文件被删
	st.mu.Lock()
	assert.Len(t, st.deleted, 1)
	assert.Contains(t, st.deleted[0], "abc.txt")
	st.mu.Unlock()

	// DB 记录已删（过期的不存在，未过期的还在）
	_, err = svc.fileCodeRepo.GetByCode(ctx, "EXP1")
	assert.Error(t, err)
	_, err = svc.fileCodeRepo.GetByCode(ctx, "OK1")
	assert.NoError(t, err)
}

// TestCleanExpiredFiles_NoStorage 无 storage 时不崩，仅删 DB
func TestCleanExpiredFiles_NoStorage(t *testing.T) {
	newAdminTestDB(t)
	svc := NewService() // 无 storage

	ctx := context.Background()
	past := time.Now().Add(-time.Hour)
	require.NoError(t, svc.fileCodeRepo.Create(ctx, &model.FileCode{
		Code: "EXP2", FilePath: "x", UUIDFileName: "y", Size: 10,
		ExpiredAt: &past, ExpiredCount: -1,
	}))

	deleted, _, err := svc.CleanExpiredFiles(ctx)
	require.NoError(t, err)
	assert.Equal(t, int64(1), deleted)
}

// --- SystemConfig 持久化 ---

// customTestConfig 构造一份与默认值不同的配置，便于断言。
func customTestConfig(name string) *SystemConfig {
	cfg := &SystemConfig{}
	cfg.Base.Name = name
	cfg.Base.Description = "自定义描述"
	cfg.Base.Port = 12345
	cfg.Storage.Type = "s3"
	cfg.Storage.MaxSize = 5 * 1024 * 1024 * 1024
	cfg.Transfer.MaxCount = 50
	cfg.Transfer.ExpireDefault = 30
	return cfg
}

// TestUpdateConfig_PersistsToDB 验证 UpdateConfig 写穿到 system_configs 表
func TestUpdateConfig_PersistsToDB(t *testing.T) {
	newAdminTestDB(t)
	svc := NewService()

	ctx := context.Background()
	require.NoError(t, svc.UpdateConfig(ctx, customTestConfig("持久化站点")))

	var rec model.SystemConfigRecord
	require.NoError(t, db.GetDB().First(&rec).Error, "应存在持久化记录")
	assert.Contains(t, rec.Data, `"name":"持久化站点"`)
}

// TestGetConfig_PersistedAcrossRestart 验证重启（新 Service 实例）后读到持久化配置
func TestGetConfig_PersistedAcrossRestart(t *testing.T) {
	newAdminTestDB(t)

	svc1 := NewService()
	require.NoError(t, svc1.UpdateConfig(context.Background(), customTestConfig("重启不丢")))

	svc2 := NewService() // 模拟重启后的新实例
	cfg, err := svc2.GetConfig(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "重启不丢", cfg.Base.Name)
	assert.Equal(t, 12345, cfg.Base.Port)
	assert.Equal(t, "s3", cfg.Storage.Type)
	assert.Equal(t, int64(5*1024*1024*1024), cfg.Storage.MaxSize)
	assert.Equal(t, 50, cfg.Transfer.MaxCount)
	assert.Equal(t, 30, cfg.Transfer.ExpireDefault)
}

// TestGetConfig_DefaultsWhenNoRecord 验证无持久化记录时返回默认值
func TestGetConfig_DefaultsWhenNoRecord(t *testing.T) {
	newAdminTestDB(t)

	cfg, err := NewService().GetConfig(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "FilesCodeBox", cfg.Base.Name)
	assert.Equal(t, "local", cfg.Storage.Type)
	assert.Equal(t, 100, cfg.Transfer.MaxCount)
}

// TestGetConfig_WithoutDB 验证 DB 未初始化时不 panic，回退默认配置
func TestGetConfig_WithoutDB(t *testing.T) {
	db.SetDatabaseInstance(nil)
	t.Cleanup(func() { db.SetDatabaseInstance(nil) })

	cfg, err := NewService().GetConfig(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "FilesCodeBox", cfg.Base.Name)
}

// TestUpdateConfig_InvalidConfig 验证非法配置被拒绝且不落库
func TestUpdateConfig_InvalidConfig(t *testing.T) {
	newAdminTestDB(t)
	svc := NewService()

	bad := customTestConfig("非法端口")
	bad.Base.Port = 70000
	require.Error(t, svc.UpdateConfig(context.Background(), bad))

	neg := customTestConfig("负数上限")
	neg.Storage.MaxSize = -1
	require.Error(t, svc.UpdateConfig(context.Background(), neg))

	// 内存与 DB 均未被污染
	cfg, err := svc.GetConfig(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "FilesCodeBox", cfg.Base.Name)
	var count int64
	require.NoError(t, db.GetDB().Model(&model.SystemConfigRecord{}).Count(&count).Error)
	assert.Equal(t, int64(0), count)
}

// TestUpdateConfig_SingleRow 验证多次更新只保留一行记录
func TestUpdateConfig_SingleRow(t *testing.T) {
	newAdminTestDB(t)
	svc := NewService()
	ctx := context.Background()

	require.NoError(t, svc.UpdateConfig(ctx, customTestConfig("第一版")))
	require.NoError(t, svc.UpdateConfig(ctx, customTestConfig("第二版")))

	var count int64
	require.NoError(t, db.GetDB().Model(&model.SystemConfigRecord{}).Count(&count).Error)
	assert.Equal(t, int64(1), count)

	cfg, err := svc.GetConfig(ctx)
	require.NoError(t, err)
	assert.Equal(t, "第二版", cfg.Base.Name)
}
