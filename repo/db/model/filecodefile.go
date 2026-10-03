package model

import (
	"path/filepath"
	"strings"

	"gorm.io/gorm"
)

// FileCodeFile 多文件分享的子文件行（P0 多文件：1 个 FileCode 分享 ↔ N 个文件）。
//
// 兼容策略：FileCode 主表保留首个文件的 legacy 字段（FilePath/UUIDFileName/Size/
// FileHash），旧单文件分享零迁移可用；新分享（含单文件）每文件写一行，
// 主表 Size 为各文件之和。子行 FilePath 存储相对路径（已含唯一文件名），
// 与 FileCode.GetFilePath() 的"目录+UUID 文件名"新格式不同——子行不做旧格式兼容。
type FileCodeFile struct {
	gorm.Model
	FileCodeID   uint   `gorm:"index;not null" json:"file_code_id"`
	FileName     string `gorm:"size:255" json:"file_name"`      // 原始文件名（消毒后，仅展示）
	UUIDFileName string `gorm:"size:255" json:"uuid_file_name"` // 磁盘/对象唯一名
	FilePath     string `gorm:"size:255" json:"file_path"`      // 存储相对路径（含唯一文件名）
	Size         int64  `gorm:"default:0" json:"size"`
	FileHash     string `gorm:"size:64" json:"file_hash"` // SHA-256（秒传/完整性校验）
	SortOrder    int    `gorm:"default:0" json:"sort_order"`
}

// GetFullPath 取存储相对路径（与 FileCode.GetFilePath 不同：子行恒为新格式，
// 直接返回 FilePath）。
func (f *FileCodeFile) GetFullPath() string {
	return f.FilePath
}

// DisplayName 展示名：优先 FileName，回退 UUID 文件名/路径末段。
func (f *FileCodeFile) DisplayName() string {
	if name := strings.TrimSpace(f.FileName); name != "" {
		return name
	}
	if f.UUIDFileName != "" {
		return f.UUIDFileName
	}
	return filepath.Base(f.FilePath)
}
