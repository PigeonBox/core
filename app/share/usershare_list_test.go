package share

import (
	"testing"

	"github.com/filescodebox/core/repo/db/model"
	"github.com/stretchr/testify/assert"
)

// 守卫测试：我的分享列表项映射的类型分类与文件名提取。
// 文件分享写侧把原始文件名存 Text（uuid_file_name 常为空），判定必须走
// model.FileCode.IsTextShare（Text 非空且无文件路径），文件名展示取原始名。
// 回归背景：IsTextShare 用 f.Text != "" 导致文件分享全被标成文本（2026-10-06）。
func TestToUserShareListItemClassification(t *testing.T) {
	t.Run("文件分享不判为文本,FileName 取原始文件名", func(t *testing.T) {
		item := toUserShareListItem(&model.FileCode{
			Code:     "FILE0001",
			FilePath: "uploads/2026/10/06/uuid-name.txt",
			Text:     "原始文件.txt",
			Size:     32,
		})
		assert.False(t, item.IsTextShare)
		assert.Equal(t, "原始文件.txt", item.FileName)
	})

	t.Run("纯文本分享判为文本,FileName 留空", func(t *testing.T) {
		item := toUserShareListItem(&model.FileCode{
			Code: "TEXT0001",
			Text: "一段普通的文本内容",
		})
		assert.True(t, item.IsTextShare)
		assert.Empty(t, item.FileName)
	})

	t.Run("历史文件分享无 Text 时 FileName 回退 UUID 名再回退路径段", func(t *testing.T) {
		item := toUserShareListItem(&model.FileCode{
			Code:         "LEGAC001",
			FilePath:     "uploads/2026/10/06",
			UUIDFileName: "fallback-uuid.txt",
		})
		assert.False(t, item.IsTextShare)
		assert.Equal(t, "fallback-uuid.txt", item.FileName)

		item = toUserShareListItem(&model.FileCode{
			Code:     "LEGAC002",
			FilePath: "uploads/2026/10/06/path-fallback.txt",
		})
		assert.False(t, item.IsTextShare)
		assert.Equal(t, "path-fallback.txt", item.FileName)
	})
}
