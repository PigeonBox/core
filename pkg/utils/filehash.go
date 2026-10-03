package utils

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
)

// HashReader 流式计算 reader 的 SHA-256，返回十六进制字符串。
// 用于大文件（分片合并后 / 上传落盘后）不占额外内存的完整性指纹。
func HashReader(r io.Reader) (string, error) {
	h := sha256.New()
	if _, err := io.Copy(h, r); err != nil {
		return "", fmt.Errorf("计算文件哈希失败: %w", err)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// HashFile 计算本地文件 SHA-256（分片合并完成后由 handler 调用，写入 file_codes.file_hash）。
func HashFile(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("打开文件失败: %w", err)
	}
	defer func() { _ = f.Close() }()
	return HashReader(f)
}

// HashBytes 计算字节切片 SHA-256（小内容场景，如分片数据）。
func HashBytes(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}
