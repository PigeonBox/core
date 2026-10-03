package moderation

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWordListModerator_Text(t *testing.T) {
	t.Run("词表空恒放行", func(t *testing.T) {
		m := NewWordListModerator(nil, "reject")
		assert.Equal(t, VerdictAllow, m.InspectText(context.Background(), "任何内容"))
	})
	t.Run("命中拒绝", func(t *testing.T) {
		m := NewWordListModerator([]string{"违禁词"}, "reject")
		assert.Equal(t, VerdictReject, m.InspectText(context.Background(), "这句包含违禁词的内容"))
		assert.Equal(t, VerdictAllow, m.InspectText(context.Background(), "正常内容"))
	})
	t.Run("大小写不敏感", func(t *testing.T) {
		m := NewWordListModerator([]string{"CASINO"}, "reject")
		assert.Equal(t, VerdictReject, m.InspectText(context.Background(), "go to casino now"))
	})
	t.Run("pending 策略", func(t *testing.T) {
		m := NewWordListModerator([]string{"可疑"}, "pending")
		assert.Equal(t, VerdictPending, m.InspectText(context.Background(), "含可疑词"))
	})
	t.Run("非法action回退reject", func(t *testing.T) {
		m := NewWordListModerator([]string{"x"}, "whatever")
		assert.Equal(t, VerdictReject, m.InspectText(context.Background(), "x"))
	})
	t.Run("空白词被忽略", func(t *testing.T) {
		m := NewWordListModerator([]string{"  ", ""}, "reject")
		assert.Equal(t, VerdictAllow, m.InspectText(context.Background(), "anything"))
	})
	t.Run("热更新词表", func(t *testing.T) {
		m := NewWordListModerator(nil, "reject")
		assert.Equal(t, VerdictAllow, m.InspectText(context.Background(), "newword"))
		m.UpdateWords([]string{"newword"})
		assert.Equal(t, VerdictReject, m.InspectText(context.Background(), "newword"))
	})
}

func TestWordListModerator_File(t *testing.T) {
	m := NewWordListModerator([]string{" anything "}, "reject")
	// v1 文件侧恒放行（钩子留位）
	assert.Equal(t, VerdictAllow, m.InspectFile(context.Background(), UploadMeta{FileName: "evil.exe"}))
}
