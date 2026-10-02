package utils

import "golang.org/x/crypto/bcrypt"

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
