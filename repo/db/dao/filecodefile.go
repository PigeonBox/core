package dao

import (
	"context"

	"github.com/filescodebox/core/repo/db"
	"github.com/filescodebox/core/repo/db/model"
)

// FileCodeFileRepository 多文件分享子文件表 DAO（P0 多文件）。
type FileCodeFileRepository struct{}

func NewFileCodeFileRepository() *FileCodeFileRepository {
	return &FileCodeFileRepository{}
}

// CreateBatch 批量写入子文件行（单事务）。
func (r *FileCodeFileRepository) CreateBatch(ctx context.Context, files []*model.FileCodeFile) error {
	if len(files) == 0 {
		return nil
	}
	return db.GetDB().WithContext(ctx).Create(&files).Error
}

// ListByFileCodeID 按 FileCodeID 列出子文件（按 SortOrder, ID 稳定排序）。
// 软删除的行自动排除。
func (r *FileCodeFileRepository) ListByFileCodeID(ctx context.Context, fileCodeID uint) ([]*model.FileCodeFile, error) {
	var files []*model.FileCodeFile
	err := db.GetDB().WithContext(ctx).
		Where("file_code_id = ?", fileCodeID).
		Order("sort_order ASC, id ASC").
		Find(&files).Error
	return files, err
}

// GetByID 取单个子文件行。
func (r *FileCodeFileRepository) GetByID(ctx context.Context, id uint) (*model.FileCodeFile, error) {
	var f model.FileCodeFile
	if err := db.GetDB().WithContext(ctx).First(&f, id).Error; err != nil {
		return nil, err
	}
	return &f, nil
}

// CountByFileCodeID 子文件行数（软删除不含）。
func (r *FileCodeFileRepository) CountByFileCodeID(ctx context.Context, fileCodeID uint) (int64, error) {
	var n int64
	err := db.GetDB().WithContext(ctx).Model(&model.FileCodeFile{}).
		Where("file_code_id = ?", fileCodeID).Count(&n).Error
	return n, err
}

// SoftDeleteByFileCodeIDs 按 FileCodeID 批量软删子文件行。
// 物理文件删除由调用方先行完成（DB 软删可回滚，物理删不可）。
func (r *FileCodeFileRepository) SoftDeleteByFileCodeIDs(ctx context.Context, fileCodeIDs []uint) error {
	if len(fileCodeIDs) == 0 {
		return nil
	}
	return db.GetDB().WithContext(ctx).
		Where("file_code_id IN ?", fileCodeIDs).
		Delete(&model.FileCodeFile{}).Error
}
