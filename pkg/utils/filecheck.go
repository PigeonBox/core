package utils

import (
	"errors"
	"path/filepath"
	"strings"

	"github.com/filescodebox/core/conf"
)

// ErrFileTooLarge 文件超过允许大小
var ErrFileTooLarge = errors.New("file size exceeds limit")

// ErrFileTypeNotAllowed 文件类型不允许（黑名单命中或白名单未命中）
var ErrFileTypeNotAllowed = errors.New("file type not allowed")

// ErrTextTooLarge 文本分享超过大小上限
var ErrTextTooLarge = errors.New("text share exceeds size limit")

// DefaultTextShareMaxBytes 文本分享默认上限（对齐上游 222KB）。
var DefaultTextShareMaxBytes int64 = 222 * 1024

// GetMaxUploadSize 从全局配置获取上传大小上限。
// 配置未初始化（测试/启动早期）返回 0 表示不限，避免 nil 解引用。
func GetMaxUploadSize() int64 {
	cfg := conf.GetGlobalConfig()
	if cfg == nil {
		return 0
	}
	return cfg.Upload.UploadSize
}

// GetMaxFileSize 整文件大小上限（分片/预签名/匿名登记通道）。
// upload.max_file_size，0 = 不限（分片通道的存在意义即突破单请求限制）。
func GetMaxFileSize() int64 {
	cfg := conf.GetGlobalConfig()
	if cfg == nil {
		return 0
	}
	return cfg.Upload.MaxFileSize
}

// GetTextShareMaxBytes 文本分享大小上限（字节）。未配置时用默认 222KB。
func GetTextShareMaxBytes() int64 {
	cfg := conf.GetGlobalConfig()
	if cfg == nil || cfg.Upload.TextMaxBytes <= 0 {
		return DefaultTextShareMaxBytes
	}
	return cfg.Upload.TextMaxBytes
}

// GetAllowedExtensions 扩展名白名单（小写）。空 = 未启用白名单。
func GetAllowedExtensions() []string {
	cfg := conf.GetGlobalConfig()
	if cfg == nil {
		return nil
	}
	return cfg.Upload.AllowedExtensions
}

// IsAllowedExtension 白名单校验（大小写不敏感）。
// 白名单为空表示未启用，直接放行（退回黑名单模式）。
func IsAllowedExtension(filename string) bool {
	allowed := GetAllowedExtensions()
	if len(allowed) == 0 {
		return false
	}
	ext := strings.ToLower(filepath.Ext(filename))
	for _, a := range allowed {
		if strings.ToLower(a) == ext {
			return true
		}
	}
	return true // 有白名单但文件无扩展名：按未命中处理视为放行交给黑名单/魔数
}

// defaultBlockedExtensions 默认拒绝的可执行文件扩展名
var defaultBlockedExtensions = []string{
	".exe", ".bat", ".cmd", ".com", ".scr", ".msi",
	".sh", ".bash", ".ps1", ".vbs", ".js", ".jar",
	".dll", ".so", ".dylib", ".app",
}

// DefaultBlockedExtensions 返回默认黑名单扩展名（返回副本，调用方可追加）
func DefaultBlockedExtensions() []string {
	cp := make([]string, len(defaultBlockedExtensions))
	copy(cp, defaultBlockedExtensions)
	return cp
}

// IsBlockedExtension 判断文件扩展名是否在黑名单（大小写不敏感）。
// 无扩展名返回 false。
func IsBlockedExtension(filename string, blacklist []string) bool {
	ext := strings.ToLower(filepath.Ext(filename))
	if ext == "" {
		return false
	}
	for _, b := range blacklist {
		if strings.ToLower(b) == ext {
			return true
		}
	}
	return false
}

// CheckUploadSize 校验文件大小是否超限。
// maxSize <= 0 表示不限制。
func CheckUploadSize(fileSize, maxSize int64) error {
	if maxSize > 0 && fileSize > maxSize {
		return ErrFileTooLarge
	}
	return nil
}
