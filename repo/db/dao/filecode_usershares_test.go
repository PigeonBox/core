package dao

import (
	"context"
	"testing"

	"github.com/filescodebox/core/repo/db/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func uintPtr(v uint) *uint { return &v }

// 守卫测试：我的分享列表类型分类。
// 写侧惯例：文件分享的原始文件名存 Text 字段（uuid_file_name 常为空），
// 因此 text/file 分类必须按 file_path 判定，Text != "" 不能作为文本分享依据——
// 否则文件分享被误归文本（「文件」tab 恒空回归，2026-10-06 实测）。
func TestGetUserSharesWithFilterTypeClassification(t *testing.T) {
	newGovernanceTestDB(t)
	repo := NewFileCodeRepository()
	ctx := context.Background()

	fileShare := &model.FileCode{
		Code:     "FILE0001",
		FilePath: "uploads/2026/10/06/uuid-name.txt",
		Text:     "原始文件.txt",
		Size:     32,
		UserID:   uintPtr(1),
	}
	textShare := &model.FileCode{
		Code:   "TEXT0001",
		Text:   "一段普通的文本内容",
		UserID: uintPtr(1),
	}
	require.NoError(t, repo.Create(ctx, fileShare))
	require.NoError(t, repo.Create(ctx, textShare))

	t.Run("file 过滤按 file_path 判定,文件分享可见", func(t *testing.T) {
		items, total, err := repo.GetUserSharesWithFilter(ctx, 1, UserShareFilter{Status: "file", Page: 1, PageSize: 20})
		require.NoError(t, err)
		assert.Equal(t, int64(1), total)
		require.Len(t, items, 1)
		assert.Equal(t, "FILE0001", items[0].Code)
	})

	t.Run("text 过滤不含文件分享", func(t *testing.T) {
		items, total, err := repo.GetUserSharesWithFilter(ctx, 1, UserShareFilter{Status: "text", Page: 1, PageSize: 20})
		require.NoError(t, err)
		assert.Equal(t, int64(1), total)
		require.Len(t, items, 1)
		assert.Equal(t, "TEXT0001", items[0].Code)
	})

	t.Run("搜索按原始文件名命中文件分享", func(t *testing.T) {
		items, total, err := repo.GetUserSharesWithFilter(ctx, 1, UserShareFilter{Status: "all", Search: "原始文件", Page: 1, PageSize: 20})
		require.NoError(t, err)
		assert.Equal(t, int64(1), total)
		require.Len(t, items, 1)
		assert.Equal(t, "FILE0001", items[0].Code)
	})
}
