package utils

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestSanitizeFileName(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"../../etc/passwd", "passwd"},                          // 路径穿越剥离
		{`C:\Windows\evil.exe`, "evil.exe"},                     // Windows 路径
		{"../../..", "file"},                                    // 纯路径段回退
		{"  hello world.txt  ", "hello world.txt"},              // 首尾空白
		{"con:test*?.txt", "con_test__.txt"},                    // Windows 保留字符
		{"报告\x00\x1f终稿.docx", "报告终稿.docx"},                      // 控制字符
		{"a.txt.", "a.txt"},                                     // 结尾点
		{strings.Repeat("长", 200) + ".pdf", strings.Repeat("长", 116) + ".pdf"}, // 截断保扩展名
		{"", "file"},
	}
	for _, tc := range cases {
		got := SanitizeFileName(tc.in)
		assert.Equal(t, tc.want, got, "input=%q", tc.in)
	}
	// 消毒后的名字绝不含路径分隔符
	for _, dirty := range []string{"a/b/c.txt", `a\b.txt`, "../x"} {
		assert.NotContains(t, SanitizeFileName(dirty), "/")
		assert.NotContains(t, SanitizeFileName(dirty), `\`)
	}
}

func TestHashReaderAndBytes(t *testing.T) {
	// SHA-256("hello")
	want := "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"
	got, err := HashReader(strings.NewReader("hello"))
	require.NoError(t, err)
	assert.Equal(t, want, got)
	assert.Equal(t, want, HashBytes([]byte("hello")))
}

func TestMatchBlockedMagic(t *testing.T) {
	// PE 头（MZ）
	_, hit := MatchBlockedMagic([]byte("MZ\x90\x00rest-of-pe"))
	assert.True(t, hit)
	// ELF 头
	_, hit = MatchBlockedMagic([]byte{0x7f, 'E', 'L', 'F', 0x02})
	assert.True(t, hit)
	// shebang
	_, hit = MatchBlockedMagic([]byte("#!/bin/sh\n"))
	assert.True(t, hit)
	// 普通 PNG 内容不命中
	_, hit = MatchBlockedMagic([]byte("\x89PNG\r\n\x1a\n"))
	assert.False(t, hit)
	// 空内容
	_, hit = MatchBlockedMagic(nil)
	assert.False(t, hit)
}

func TestCheckUploadContentWhitelistPriority(t *testing.T) {
	// 无全局配置时白名单不生效，走黑名单 + 魔数
	assert.ErrorIs(t, CheckUploadContent("evil.exe", nil), ErrFileTypeNotAllowed)
	assert.NoError(t, CheckUploadContent("doc.pdf", []byte("%PDF-1.4")))
	// 魔数命中：改扩展名伪装被拦
	_, isMagic := CheckUploadContent("cat.jpg", []byte("MZ\x90\x00")).(*MagicMismatchError)
	assert.True(t, isMagic, "MZ 内容伪装 jpg 应被魔数拦截")
	// 文本分享大小上限默认值
	assert.Equal(t, int64(222*1024), GetTextShareMaxBytes())
}
