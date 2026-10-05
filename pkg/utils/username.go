package utils

import (
	"errors"
	"fmt"
)

// ValidateUsername 用户名字符集白名单（2026-10-05 审计 P3）：
// 2-32 位，仅字母/数字/下划线/连字符/点。username 进锁定键、限流键、
// 审计日志与存储路径拼接，收紧字符集防控制字符/超长/同形字符注入。
// 已有老用户不受影响（仅约束新建）。
func ValidateUsername(name string) error {
	if len(name) < 2 || len(name) > 32 {
		return errors.New("用户名长度需为 2-32 位")
	}
	for _, r := range name {
		switch {
		case r >= '0' && r <= '9':
		case r >= 'a' && r <= 'z':
		case r >= 'A' && r <= 'Z':
		case r == '_' || r == '-' || r == '.':
		default:
			return fmt.Errorf("用户名仅支持字母/数字/下划线/连字符/点")
		}
	}
	return nil
}
