package share

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/filescodebox/core/pkg/utils"
)

// 回归：/share/metadata 语义——查询不扣次数、不要求密码、不外泄文本内容/密码哈希。
func TestGetShareMetadata(t *testing.T) {
	svc, _, _, _ := newTestService(t)
	ctx := context.Background()

	// 文本分享：type=text，不返回内容
	textResp, err := svc.ShareTextWithAuth(ctx, "机密内容ABC", 1, "day", false, "", nil, "127.0.0.1", false, "")
	require.NoError(t, err)
	meta, err := svc.GetShareMetadata(ctx, textResp.Code)
	require.NoError(t, err)
	require.Equal(t, "text", meta.Type)
	require.Empty(t, meta.Name, "text 分享不得经 metadata 泄露内容")
	require.False(t, meta.HasPassword)

	// 带密码的文件分享：type=file、文件名来自 Text、has_password=true
	pwdHash, err := utils.HashPassword("s3cret")
	require.NoError(t, err)
	fileResp, err := svc.CreateShare(ctx, &ShareFileReq{
		Channel:      "direct",
		FilePath:     "uploads/2026/10/03/uuid.txt",
		Size:         123,
		Text:         "a.txt",
		ExpiredAt:    utils.CalculateExpireTime(1, "day"),
		ExpiredCount: utils.CalculateExpireCount("day", 1),
		RequireAuth:  true,
		PasswordHash: pwdHash,
		UploadType:   "authenticated",
	})
	require.NoError(t, err)
	meta, err = svc.GetShareMetadata(ctx, fileResp.Code)
	require.NoError(t, err)
	require.Equal(t, "file", meta.Type)
	require.Equal(t, "a.txt", meta.Name)
	require.True(t, meta.HasPassword)
	// CreateShare 会为每个文件分享写一条子表行（单/多文件统一模型），file_count=1
	require.Equal(t, int64(1), meta.FileCount)

	// 查询不扣次数
	require.Equal(t, meta.UsedCount, func() int {
		m, _ := svc.GetShareMetadata(ctx, fileResp.Code)
		return m.UsedCount
	}())

	// 已过期：报错（handler 统一转 404）
	past := time.Now().Add(-time.Hour)
	expired, err := svc.CreateShare(ctx, &ShareFileReq{
		Channel:      "direct",
		FilePath:     "uploads/2026/10/03/old.txt",
		Size:         1,
		Text:         "old.txt",
		ExpiredAt:    &past,
		ExpiredCount: -1,
		UploadType:   "authenticated",
	})
	require.NoError(t, err)
	_, err = svc.GetShareMetadata(ctx, expired.Code)
	require.Error(t, err, "过期分享应拒绝元数据查询")
}
