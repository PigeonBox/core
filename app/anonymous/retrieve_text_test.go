package anonymous

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/pigeonbox/core/repo/db/model"
)

// TestRetrieve_TextShareFlag 文本分享取件标注 IsText（2026-10-08 "只保留 6 位码"）：
// handler 据此回传 share_code，前端取件结果页跳详情页渲染文本；文件分享恒 false。
func TestRetrieve_TextShareFlag(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()

	require.NoError(t, svc.fileCodeRepo.Create(ctx, &model.FileCode{
		Code: "TEXTABC", Text: "一段文本内容", ExpiredCount: -1,
	}))
	require.NoError(t, svc.fileCodeRepo.Create(ctx, &model.FileCode{
		Code: "FILEABC", FilePath: "uploads/x/FILEABC.bin", ExpiredCount: -1,
	}))

	mint := func(share string) string {
		expireAt := time.Now().Add(time.Hour)
		code, err := svc.MintForShare(ctx, share, "preview", 8, false, &expireAt)
		require.NoError(t, err)
		require.Len(t, code, codeLength)
		return code
	}

	textMeta, err := svc.Retrieve(ctx, mint("TEXTABC"), "")
	require.NoError(t, err)
	assert.True(t, textMeta.IsText, "文本分享 Retrieve 应标注 IsText")
	assert.Equal(t, "TEXTABC", textMeta.ShareCode)

	fileMeta, err := svc.Retrieve(ctx, mint("FILEABC"), "")
	require.NoError(t, err)
	assert.False(t, fileMeta.IsText)
}
