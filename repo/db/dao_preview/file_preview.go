package dao_preview

import (
	"context"

	"github.com/pigeonbox/core/repo/db"
	"github.com/pigeonbox/core/repo/db/model"
	"gorm.io/gorm"
)

// FilePreviewRepository 预览信息仓库
type FilePreviewRepository struct {
	conn *gorm.DB // nil = 走全局 db.GetDB()（兼容历史无参构造）；非 nil = 注入实例
}

// NewFilePreviewRepository 创建预览仓库
// NewFilePreviewRepository 构造 repository；可选注入 *gorm.DB（测试隔离/嵌入式场景），缺省走全局 db.GetDB()。注入约定见 doc.go。
func NewFilePreviewRepository(gormDB ...*gorm.DB) *FilePreviewRepository {
	r := &FilePreviewRepository{}
	if len(gormDB) > 0 {
		r.conn = gormDB[0]
	}
	return r
}

func (r *FilePreviewRepository) db() *gorm.DB {
	if r != nil && r.conn != nil { // r != nil 守卫：兼容历史 nil receiver 直调，见 doc.go
		return r.conn
	}
	return db.GetDB()
}

// Create 创建预览信息
func (r *FilePreviewRepository) Create(ctx context.Context, preview *model.FilePreview) error {
	return r.db().WithContext(ctx).Create(preview).Error
}

// GetByFileCodeID 根据文件ID获取预览信息
func (r *FilePreviewRepository) GetByFileCodeID(ctx context.Context, fileCodeID uint) (*model.FilePreview, error) {
	var preview model.FilePreview
	err := r.db().WithContext(ctx).Where("file_code_id = ?", fileCodeID).First(&preview).Error
	if err != nil {
		return nil, err
	}
	return &preview, nil
}

// Update 更新预览信息
func (r *FilePreviewRepository) Update(ctx context.Context, preview *model.FilePreview) error {
	return r.db().WithContext(ctx).Save(preview).Error
}

// Delete 删除预览信息
func (r *FilePreviewRepository) Delete(ctx context.Context, fileCodeID uint) error {
	return r.db().WithContext(ctx).Where("file_code_id = ?", fileCodeID).Delete(&model.FilePreview{}).Error
}
