package utils

import (
	"bytes"
	"strings"

	"github.com/pigeonbox/core/pkg/metrics"
)

// magicSignature 内容魔数签名（前缀匹配）。
// 对标上游 2.5.x 的魔数校验：扩展名可以伪造，可执行格式的内容头不能。
var magicSignatures = []struct {
	name string
	pfx  []byte
}{
	{"Windows PE (exe/dll/sys)", []byte("MZ")},
	{"ELF (linux executable)", []byte{0x7f, 'E', 'L', 'F'}},
	{"Mach-O (macOS executable)", []byte{0xfe, 0xed, 0xfa, 0xce}},
	{"Mach-O 64-bit", []byte{0xfe, 0xed, 0xfa, 0xcf}},
	{"Mach-O (byte-swapped)", []byte{0xce, 0xfa, 0xed, 0xfe}},
	{"Mach-O 64-bit (byte-swapped)", []byte{0xcf, 0xfa, 0xed, 0xfe}},
	{"shell script", []byte("#!")},
	{"Java class", []byte{0xca, 0xfe, 0xba, 0xbe}},
	{"Windows shortcut", []byte{0x4c, 0x00, 0x00, 0x00, 0x01, 0x14, 0x02, 0x00}},
}

// magicCheckSkipExts 这些"合法内容本身是可执行格式"的扩展名跳过魔数检查。
// 例：.zip 内容以 PK 开头不在黑名单；.msi 本质是 OLE 复合文档，已被扩展名黑名单拦截。
var magicCheckSkipExts = map[string]bool{}

// MatchBlockedMagic 检查内容头部是否命中可执行文件魔数。
// head 为文件（或首个分片）的前若干字节。
func MatchBlockedMagic(head []byte) (string, bool) {
	for _, sig := range magicSignatures {
		if bytes.HasPrefix(head, sig.pfx) {
			return sig.name, true
		}
	}
	return "", false
}

// CheckUploadContent 组合校验：扩展名准入（白/黑名单）+ 魔数。
//   - IsAllowedExtension（白名单未命中 / 黑名单命中）→ 拒绝
//   - 魔数命中（开关 upload.enable_magic_check 开、head 非空、扩展名不在 skip 列表）→ 拒绝
//     防"jpg.exe 改名 cat.jpg"
//
// head 可为 nil（调用方拿不到内容头时只做扩展名检查）。
func CheckUploadContent(filename string, head []byte) error {
	if !IsAllowedExtension(filename) {
		metrics.RecordRejected(metrics.RejectType)
		return ErrFileTypeNotAllowed
	}
	if len(head) > 0 && GetEnableMagicCheck() {
		ext := strings.ToLower(filenameExt(filename))
		if !magicCheckSkipExts[ext] {
			if name, hit := MatchBlockedMagic(head); hit {
				metrics.RecordRejected(metrics.RejectType)
				return &MagicMismatchError{Signature: name}
			}
		}
	}
	return nil
}

// MagicMismatchError 扩展名与内容魔数不符
type MagicMismatchError struct {
	Signature string
}

func (e *MagicMismatchError) Error() string {
	return "文件内容与扩展名不符（检测到 " + e.Signature + "）"
}

func filenameExt(name string) string {
	dot := strings.LastIndexByte(name, '.')
	if dot < 0 {
		return ""
	}
	return name[dot:]
}
