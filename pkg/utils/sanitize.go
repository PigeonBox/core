package utils

import (
	"path/filepath"
	"strings"
	"unicode"
)

// maxFileNameLength 文件名最大长度（含扩展名）。
// 覆盖常见文件系统 255 字节上限的一半，兼顾 UTF-8 多字节字符。
const maxFileNameLength = 120

// SanitizeFileName 消毒用户提供的原始文件名：
//   - 剥离路径成分（/ \ ..），防止路径穿越
//   - 去除控制字符与 Windows 保留字符 : * ? " < > |
//   - 去除首尾空白与结尾的点（Windows 目录项陷阱）
//   - 空名回退为 "file"
//   - 超长截断（保留扩展名）
//
// 返回值仅用于"展示/存储原始名"，磁盘文件名仍由 UUID 生成，双保险。
func SanitizeFileName(name string) string {
	// 只取最后一段路径（兼容 \ 与 /），剥掉任何目录成分
	if idx := strings.LastIndexAny(name, `/\`); idx >= 0 {
		name = name[idx+1:]
	}
	name = strings.TrimSpace(name)

	var b strings.Builder
	for _, r := range name {
		switch {
		case r < 0x20 || r == 0x7f:
			// 控制字符直接丢弃
		case r == ':' || r == '*' || r == '?' || r == '"' || r == '<' || r == '>' || r == '|':
			b.WriteRune('_')
		case r == '.' || r == '_' || r == '-' || r == ' ' || unicode.IsLetter(r) || unicode.IsDigit(r):
			b.WriteRune(r)
		default:
			// 其余可打印字符保留（中文、日文等），不可打印丢弃
			if unicode.IsPrint(r) {
				b.WriteRune(r)
			}
		}
	}
	name = strings.Trim(b.String(), ". ")

	if name == "" {
		return "file"
	}

	// 超长截断，保留扩展名
	ext := filepath.Ext(name)
	if len(name) > maxFileNameLength {
		stem := strings.TrimSuffix(name, ext)
		keep := maxFileNameLength - len(ext)
		if keep < 1 {
			keep = 1
		}
		// 按 rune 截断避免砍断多字节字符
		runes := []rune(stem)
		if len(runes) > keep {
			runes = runes[:keep]
		}
		name = string(runes) + ext
	}
	return name
}
