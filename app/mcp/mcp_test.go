package mcp

import (
	"context"
	"encoding/json"
	"io"
	"mime/multipart"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	adminApp "github.com/filescodebox/core/app/admin"
	shareApp "github.com/filescodebox/core/app/share"
	"github.com/filescodebox/core/repo/db"
	"github.com/filescodebox/core/repo/db/dao"
	"github.com/filescodebox/core/repo/db/model"
	"github.com/filescodebox/core/storage"
)

type nopStorage struct{}

func (nopStorage) SaveFile(_ context.Context, _ *multipart.FileHeader, _ string) (*storage.FileOperationResult, error) {
	return &storage.FileOperationResult{Success: true}, nil
}
func (nopStorage) DeleteFile(_ context.Context, _ string) error    { return nil }
func (nopStorage) GetFile(_ context.Context, _ string) ([]byte, error) { return nil, nil }
func (nopStorage) FileExists(_ context.Context, _ string) bool     { return true }
func (nopStorage) SaveStream(_ context.Context, _ string, _ io.Reader, _ int64) (int64, error) { return 0, nil }
func (nopStorage) SaveChunk(_ context.Context, _ string, _ int, _ []byte) error { return nil }
func (nopStorage) MergeChunks(_ context.Context, _ string, _ int, _ string) error { return nil }
func (nopStorage) CleanChunks(_ context.Context, _ string) error   { return nil }
func (nopStorage) GetFileSize(_ context.Context, _ string) (int64, error) { return 0, nil }
func (nopStorage) GetFileURL(_ context.Context, _ string) (string, error) { return "", nil }
func (nopStorage) GetFileReader(_ context.Context, _ string) (io.ReadCloser, int64, error) {
	return nil, 0, nil
}

// shareAPIAdapter 测试桥：mcp.ShareAPI 接真实 share.Service
// （生产由 bootstrap 的 mcpShareAdapter 注入同形适配器；测试文件允许跨层 import）。
type shareAPIAdapter struct{ svc *shareApp.Service }

func (a shareAPIAdapter) CreateTextShare(ctx context.Context, text string, expireValue int, expireStyle string,
	requireAuth bool, passwordHash string, ownerIP, customCode string) (string, string, error) {
	resp, err := a.svc.ShareTextWithAuth(ctx, text, expireValue, expireStyle, requireAuth, passwordHash, nil, ownerIP, false, customCode)
	if err != nil {
		return "", "", err
	}
	return resp.Code, resp.FullShareURL, nil
}

func (a shareAPIAdapter) ShareFiles(ctx context.Context, code string) ([]ShareFileInfo, error) {
	items, err := a.svc.ListShareFiles(ctx, code)
	if err != nil {
		return nil, err
	}
	out := make([]ShareFileInfo, len(items))
	for i, it := range items {
		out[i] = ShareFileInfo{Name: it.Name, Size: it.Size}
	}
	return out, nil
}

// adminAPIAdapter 测试桥：mcp.AdminAPI 接真实 admin.Service
// （生产由 bootstrap 的 mcpAdminAdapter 注入同形适配器）。
type adminAPIAdapter struct{ svc *adminApp.Service }

func (a adminAPIAdapter) DeleteShareByID(ctx context.Context, id uint) error {
	return a.svc.DeleteFile(ctx, id)
}

func (a adminAPIAdapter) SystemStats(ctx context.Context) (*SystemStats, error) {
	st, err := a.svc.GetStats(ctx)
	if err != nil {
		return nil, err
	}
	return &SystemStats{
		TotalFiles: st.TotalFiles, TotalUsers: st.TotalUsers, TotalSize: st.TotalSize,
		TodayUploads: st.TodayUploads, ExpiredFiles: st.ExpiredFiles,
	}, nil
}

func (a adminAPIAdapter) StorageStatus(ctx context.Context) (*StorageStatusInfo, error) {
	st, err := a.svc.GetStorageStatus(ctx)
	if err != nil {
		return nil, err
	}
	return &StorageStatusInfo{
		StorageType: st.StorageType, TotalSpace: st.TotalSpace, UsedSpace: st.UsedSpace,
		UsagePercent: st.UsagePercent, FileCount: st.FileCount,
	}, nil
}

func (a adminAPIAdapter) Users(ctx context.Context, page, pageSize int) ([]UserRow, int64, error) {
	users, total, err := a.svc.GetUsers(ctx, page, pageSize)
	if err != nil {
		return nil, 0, err
	}
	rows := make([]UserRow, len(users))
	for i, u := range users {
		rows[i] = UserRow{ID: u.ID, Username: u.Username, Email: u.Email, Status: u.Status}
	}
	return rows, total, nil
}

func (a adminAPIAdapter) CleanExpired(ctx context.Context) (int64, int64, error) {
	return a.svc.CleanExpiredFiles(ctx)
}

func newMCPTestService(t *testing.T) *Service {
	t.Helper()
	g, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	require.NoError(t, g.AutoMigrate(&model.FileCode{}, &model.User{}, &model.AdminOperationLog{}))
	db.SetDatabaseInstance(g)
	t.Cleanup(func() { db.SetDatabaseInstance(nil) })

	svc := NewService("test-1.0.0")
	svc.SetShareService(shareAPIAdapter{shareApp.NewService("http://test.local", nopStorage{})})
	svc.SetAdminService(adminAPIAdapter{adminApp.NewService()})
	return svc
}

// rpc 发送 JSON-RPC 请求并解析响应
func rpc(t *testing.T, s *Service, method string, params any) (status int, out map[string]any) {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": method, "params": params})
	st, raw := s.Handle(context.Background(), body)
	require.NotEmpty(t, raw, "%s 应返回响应体", method)
	require.NoError(t, json.Unmarshal(raw, &out))
	return st, out
}

func TestInitialize(t *testing.T) {
	s := newMCPTestService(t)
	st, out := rpc(t, s, "initialize", map[string]any{
		"protocolVersion": "2025-03-26",
		"clientInfo":      map[string]any{"name": "test-client", "version": "0"},
	})
	assert.Equal(t, 200, st)
	result := out["result"].(map[string]any)
	assert.Equal(t, protocolVersion, result["protocolVersion"])
	assert.Equal(t, serverName, result["serverInfo"].(map[string]any)["name"])
	assert.Contains(t, result["capabilities"].(map[string]any), "tools")
}

func TestNotification_NoResponse(t *testing.T) {
	s := newMCPTestService(t)
	body := []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	st, raw := s.Handle(context.Background(), body)
	assert.Equal(t, 202, st)
	assert.Empty(t, raw, "通知不返回响应体")
}

func TestToolsList_EightTools(t *testing.T) {
	s := newMCPTestService(t)
	_, out := rpc(t, s, "tools/list", nil)
	tools := out["result"].(map[string]any)["tools"].([]any)
	assert.Len(t, tools, 8)
	names := map[string]bool{}
	for _, tl := range tools {
		names[tl.(map[string]any)["name"].(string)] = true
	}
	for _, want := range []string{"share_text", "get_share", "list_shares", "delete_share",
		"get_system_status", "get_storage_info", "list_users", "cleanup_expired"} {
		assert.True(t, names[want], "缺少工具 %s", want)
	}
}

func TestToolsCall_TextShareRoundtrip(t *testing.T) {
	s := newMCPTestService(t)

	// share_text 创建
	_, out := rpc(t, s, "tools/call", map[string]any{
		"name": "share_text",
		"arguments": map[string]any{
			"text":         "mcp-e2e-content",
			"expire_value": 1,
			"expire_style": "day",
		},
	})
	result := out["result"].(map[string]any)
	assert.NotEqual(t, true, result["isError"])
	content := result["content"].([]any)[0].(map[string]any)["text"].(string)
	assert.Contains(t, content, "分享创建成功")

	// 提取取件码（"取件码: XXXXXXXX"）
	code := extractCode(content)
	require.Len(t, code, 8)

	// get_share 查询
	_, out = rpc(t, s, "tools/call", map[string]any{
		"name": "get_share", "arguments": map[string]any{"code": code},
	})
	info := out["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	assert.Contains(t, info, code)
	assert.Contains(t, info, "文本")

	// list_shares 可见
	_, out = rpc(t, s, "tools/call", map[string]any{"name": "list_shares"})
	listing := out["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	assert.Contains(t, listing, code)

	// delete_share 删除
	_, out = rpc(t, s, "tools/call", map[string]any{
		"name": "delete_share", "arguments": map[string]any{"code": code},
	})
	deleted := out["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	assert.Contains(t, deleted, "已删除")

	// 删除后查不到
	_, out = rpc(t, s, "tools/call", map[string]any{
		"name": "get_share", "arguments": map[string]any{"code": code},
	})
	assert.Equal(t, true, out["result"].(map[string]any)["isError"])
}

func extractCode(content string) string {
	marker := "取件码: "
	idx := strings.Index(content, marker)
	if idx < 0 {
		return ""
	}
	rest := content[idx+len(marker):]
	if len(rest) < 8 {
		return ""
	}
	return rest[:8]
}

func TestToolsCall_TextSharePassword(t *testing.T) {
	s := newMCPTestService(t)
	_, out := rpc(t, s, "tools/call", map[string]any{
		"name": "share_text",
		"arguments": map[string]any{
			"text":     "secret",
			"password": "pw123456",
		},
	})
	content := out["result"].(map[string]any)["content"].([]any)[0].(map[string]any)["text"].(string)
	code := extractCode(content)
	require.Len(t, code, 8)

	// 密码保护标记落库
	fc, err := dao.NewFileCodeRepository().GetByCode(context.Background(), code)
	require.NoError(t, err)
	assert.True(t, fc.RequireAuth)
	assert.NotEmpty(t, fc.PasswordHash)
}

func TestToolsCall_UnknownTool(t *testing.T) {
	s := newMCPTestService(t)
	_, out := rpc(t, s, "tools/call", map[string]any{"name": "nope"})
	assert.Equal(t, true, out["result"].(map[string]any)["isError"])
}

func TestProtocolErrors(t *testing.T) {
	s := newMCPTestService(t)

	// 非法 JSON → -32700
	st, raw := s.Handle(context.Background(), []byte("{not-json"))
	assert.Equal(t, 200, st)
	var resp map[string]any
	require.NoError(t, json.Unmarshal(raw, &resp))
	assert.Equal(t, float64(errParse), resp["error"].(map[string]any)["code"])

	// 未知方法 → -32601
	st, out := rpc(t, s, "resources/list", nil)
	assert.Equal(t, 200, st)
	assert.Equal(t, float64(errMethodNF), out["error"].(map[string]any)["code"])

	// ping → 空对象
	_, out = rpc(t, s, "ping", nil)
	assert.NotNil(t, out["result"])
}
