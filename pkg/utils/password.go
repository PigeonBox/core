package utils

import (
	"errors"

	"golang.org/x/crypto/bcrypt"
)

// ErrPasswordRequired 开启密码保护但未提供密码（调用方映射为 400）。
var ErrPasswordRequired = errors.New("password required")

// ResolveSharePassword 分享密码守卫的统一收口：
// requireAuth=false 返回空哈希；requireAuth=true 时密码为空返回
// ErrPasswordRequired，否则返回 bcrypt 哈希（哈希失败返回底层错误）。
func ResolveSharePassword(requireAuth bool, password string) (string, error) {
	if !requireAuth {
		return "", nil
	}
	if password == "" {
		return "", ErrPasswordRequired
	}
	return HashPassword(password)
}

// HashPassword 用 bcrypt(cost=10) 哈希密码。
// 空密码返回空字符串（表示"无密码"），不哈希。
func HashPassword(pw string) (string, error) {
	if pw == "" {
		return "", nil
	}
	b, err := bcrypt.GenerateFromPassword([]byte(pw), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// CheckPassword 校验密码。
// 空 hash 一律拒绝:启用密码保护的分享若哈希缺失(配置错误/历史脏数据),
// 不允许任何密码(包括空密码)通过——防止"空哈希绕过"。
func CheckPassword(hash, pw string) bool {
	if hash == "" {
		return false
	}
	return bcrypt.CompareHashAndPassword([]byte(hash), []byte(pw)) == nil
}
