package utils

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestHashPassword_NonEmpty(t *testing.T) {
	hash, err := HashPassword("secret123")
	require.NoError(t, err)
	assert.NotEqual(t, "secret123", hash)
	assert.Len(t, hash, 60) // bcrypt cost=10 → 60 字符
}

func TestHashPassword_UniqueSalts(t *testing.T) {
	h1, _ := HashPassword("same")
	h2, _ := HashPassword("same")
	assert.NotEqual(t, h1, h2) // 盐不同
}

func TestHashPassword_EmptyReturnsEmpty(t *testing.T) {
	hash, err := HashPassword("")
	require.NoError(t, err)
	assert.Equal(t, "", hash)
}

func TestCheckPassword_Correct(t *testing.T) {
	hash, _ := HashPassword("mypass")
	assert.True(t, CheckPassword(hash, "mypass"))
}

func TestCheckPassword_Wrong(t *testing.T) {
	hash, _ := HashPassword("mypass")
	assert.False(t, CheckPassword(hash, "wrong"))
}

func TestCheckPassword_EmptyHashRejectsAll(t *testing.T) {
	// 安全回归(2026-10 修复):空 hash 一律拒绝。
	// 修复前 CheckPassword("", "") 放行,导致 require_auth=true 但哈希未入库的分享
	// 可被空密码直接下载(密码保护被完全绕过)。
	assert.False(t, CheckPassword("", ""))
	assert.False(t, CheckPassword("", "anything"))
}

func TestCheckPassword_EmptyPasswordAgainstRealHash(t *testing.T) {
	hash, _ := HashPassword("mypass")
	assert.False(t, CheckPassword(hash, ""))
}
