package dao

import (
	"context"
	"time"

	"github.com/pigeonbox/core/repo/db"
	"github.com/pigeonbox/core/repo/db/model"
	"gorm.io/gorm"
)

type FileCodeRepository struct {
}

func NewFileCodeRepository() *FileCodeRepository {
	return &FileCodeRepository{}
}

func (r *FileCodeRepository) db() *gorm.DB {
	return db.GetDB()
}

func (r *FileCodeRepository) Create(ctx context.Context, fileCode *model.FileCode) error {
	return r.db().WithContext(ctx).Create(fileCode).Error
}

func (r *FileCodeRepository) GetByID(ctx context.Context, id uint) (*model.FileCode, error) {
	var fileCode model.FileCode
	err := r.db().WithContext(ctx).First(&fileCode, id).Error
	if err != nil {
		return nil, err
	}
	return &fileCode, nil
}

func (r *FileCodeRepository) GetByCode(ctx context.Context, code string) (*model.FileCode, error) {
	var fileCode model.FileCode
	err := r.db().WithContext(ctx).Where("code = ?", code).First(&fileCode).Error
	if err != nil {
		return nil, err
	}
	return &fileCode, nil
}

// GetByCodeFolded 大小写折叠检索（UPPER 归一；码表全 ASCII，sqlite/mysql/pg 通吃）。
// 调用方必须先精确 GetByCode 未命中再兜底本方法，热路径不恒走函数查询；
// 多行同码不同大小写时按 id 升序取最早一条，保证结果确定性。
func (r *FileCodeRepository) GetByCodeFolded(ctx context.Context, code string) (*model.FileCode, error) {
	var fileCode model.FileCode
	err := r.db().WithContext(ctx).
		Where("UPPER(code) = UPPER(?)", code).
		Order("id ASC").First(&fileCode).Error
	if err != nil {
		return nil, err
	}
	return &fileCode, nil
}

// GetByHashAndSize 秒传检索：仅命中"正常态 + 无密码"的分享。
// 回归（2026-10-03）：不过滤 status 会把 blocked/待审分享当秒传源（存在性
// oracle + 假成功 UX）；不过滤 require_auth 会让持同哈希文件者借令牌穿透
// 原分享的密码校验。
func (r *FileCodeRepository) GetByHashAndSize(ctx context.Context, fileHash string, size int64) (*model.FileCode, error) {
	var fileCode model.FileCode
	err := r.db().WithContext(ctx).
		Where("file_hash = ? AND size = ? AND deleted_at IS NULL AND status = ? AND require_auth = ?",
			fileHash, size, model.StatusNormal, false).
		First(&fileCode).Error
	if err != nil {
		return nil, err
	}
	return &fileCode, nil
}

// GetByHash 已删除：与 GetByHashAndSize SQL 逐字重复且全库无调用方（秒传走 GetByHashAndSize）。

func (r *FileCodeRepository) Update(ctx context.Context, fileCode *model.FileCode) error {
	return r.db().WithContext(ctx).Save(fileCode).Error
}

func (r *FileCodeRepository) UpdateColumns(ctx context.Context, id uint, updates map[string]interface{}) error {
	return r.db().WithContext(ctx).Model(&model.FileCode{}).Where("id = ?", id).Updates(updates).Error
}

func (r *FileCodeRepository) Delete(ctx context.Context, id uint) error {
	return r.db().WithContext(ctx).Delete(&model.FileCode{}, id).Error
}

func (r *FileCodeRepository) DeleteByFileCode(ctx context.Context, fileCode *model.FileCode) error {
	return r.db().WithContext(ctx).Delete(fileCode).Error
}

func (r *FileCodeRepository) Count(ctx context.Context) (int64, error) {
	var count int64
	err := r.db().WithContext(ctx).Model(&model.FileCode{}).Count(&count).Error
	return count, err
}

// CountToday 已删除：全库无调用方（统计走 CountTodayUploads/CountCreatedBetween）。

func (r *FileCodeRepository) CountActive(ctx context.Context) (int64, error) {
	var count int64
	err := r.db().WithContext(ctx).Model(&model.FileCode{}).
		Where("expired_at IS NULL OR expired_at > ? OR expired_count > 0", time.Now()).
		Count(&count).Error
	return count, err
}

func (r *FileCodeRepository) GetTotalSize(ctx context.Context) (int64, error) {
	var totalSize int64
	err := r.db().WithContext(ctx).Model(&model.FileCode{}).Select("COALESCE(SUM(size), 0)").Scan(&totalSize).Error
	return totalSize, err
}

func (r *FileCodeRepository) CountTodayUploads(ctx context.Context) (int64, error) {
	var count int64
	today := time.Now().Format("2006-01-02")
	err := r.db().WithContext(ctx).Model(&model.FileCode{}).Where("created_at >= ?", today).Count(&count).Error
	return count, err
}

func (r *FileCodeRepository) List(ctx context.Context, page, pageSize int, search string) ([]*model.FileCode, int64, error) {
	page, pageSize = clampPage(page, pageSize, MaxPageSize)

	query := r.db().WithContext(ctx).Model(&model.FileCode{})

	// 搜索条件
	if search != "" {
		pattern, esc := LikeContains(search)
		query = query.Where("code LIKE ? OR prefix LIKE ? OR suffix LIKE ? "+esc,
			pattern, pattern, pattern)
	}

	return paginate[model.FileCode](query.Order("created_at DESC"), page, pageSize)
}

func (r *FileCodeRepository) GetExpiredFiles(ctx context.Context) ([]*model.FileCode, error) {
	var expiredFiles []*model.FileCode
	err := r.db().WithContext(ctx).Where("(expired_at IS NOT NULL AND expired_at < ?) OR expired_count = 0", time.Now()).
		Find(&expiredFiles).Error
	return expiredFiles, err
}

func (r *FileCodeRepository) DeleteExpiredFiles(ctx context.Context, expiredFiles []*model.FileCode) (int, error) {
	if len(expiredFiles) == 0 {
		return 0, nil
	}

	count := 0
	for _, file := range expiredFiles {
		if err := r.db().WithContext(ctx).Delete(file).Error; err != nil {
			continue // 记录错误但继续处理其他文件
		}
		count++
	}
	return count, nil
}
