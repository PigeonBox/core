package dao

import (
	"context"
	"fmt"
	"time"

	"github.com/filescodebox/core/repo/db"
	"github.com/filescodebox/core/repo/db/model"
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

func (r *FileCodeRepository) List(ctx context.Context, page, pageSize int, search string) ([]*model.FileCode, int64, error) {
	page, pageSize = clampPage(page, pageSize, MaxPageSize)

	query := r.db().WithContext(ctx).Model(&model.FileCode{})

	// 搜索条件
	if search != "" {
		searchPattern := "%" + search + "%"
		query = query.Where("code LIKE ? OR prefix LIKE ? OR suffix LIKE ?",
			searchPattern, searchPattern, searchPattern)
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

func (r *FileCodeRepository) CheckCodeExists(ctx context.Context, code string, excludeID uint) (bool, error) {
	var existingFile model.FileCode
	err := r.db().WithContext(ctx).Where("code = ? AND id != ?", code, excludeID).First(&existingFile).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// GetByHash 已删除：与 GetByHashAndSize SQL 逐字重复且全库无调用方（秒传走 GetByHashAndSize）。

func (r *FileCodeRepository) CountByUserID(ctx context.Context, userID uint) (int64, error) {
	var count int64
	err := r.db().WithContext(ctx).Model(&model.FileCode{}).Where("user_id = ?", userID).Count(&count).Error
	return count, err
}

func (r *FileCodeRepository) GetTotalSizeByUserID(ctx context.Context, userID uint) (int64, error) {
	var totalSize int64
	err := r.db().WithContext(ctx).Model(&model.FileCode{}).
		Where("user_id = ?", userID).
		Select("COALESCE(SUM(size), 0)").
		Scan(&totalSize).Error
	return totalSize, err
}

func (r *FileCodeRepository) GetByUserID(ctx context.Context, userID uint, fileID uint) (*model.FileCode, error) {
	var fileCode model.FileCode
	err := r.db().WithContext(ctx).Where("id = ? AND user_id = ?", fileID, userID).First(&fileCode).Error
	if err != nil {
		return nil, err
	}
	return &fileCode, nil
}

func (r *FileCodeRepository) GetFilesByUserID(ctx context.Context, userID uint) ([]*model.FileCode, error) {
	var files []*model.FileCode
	err := r.db().WithContext(ctx).Where("user_id = ?", userID).Order("created_at DESC").Find(&files).Error
	return files, err
}

func (r *FileCodeRepository) GetFilesByUserIDWithPagination(ctx context.Context, userID uint, page, pageSize int) ([]*model.FileCode, int64, error) {
	page, pageSize = clampPage(page, pageSize, MaxPageSize)

	// 构建查询条件
	query := r.db().WithContext(ctx).Model(&model.FileCode{}).Where("user_id = ?", userID)

	return paginate[model.FileCode](query.Order("created_at DESC"), page, pageSize)
}

func (r *FileCodeRepository) DeleteByUserID(ctx context.Context, userID uint) error {
	return r.db().WithContext(ctx).Where("user_id = ?", userID).Delete(&model.FileCode{}).Error
}

func (r *FileCodeRepository) CountTodayUploads(ctx context.Context) (int64, error) {
	var count int64
	today := time.Now().Format("2006-01-02")
	err := r.db().WithContext(ctx).Model(&model.FileCode{}).Where("created_at >= ?", today).Count(&count).Error
	return count, err
}

// UserShareFilter 用户分享列表筛选条件
type UserShareFilter struct {
	Status   string // all / active / expired / text / file / deleted
	Search   string // 模糊搜索 code / 文件名
	Page     int
	PageSize int
}

// GetUserSharesWithFilter 获取用户的分享列表（带筛选）
//   - "active": 未过期且有剩余次数
//   - "expired": 时间过期 或 次数用尽
//   - "text": 文本分享（Text != ""）
//   - "file": 文件分享（Text == ""）
//   - "deleted": 软删除的（deleted_at != null）
//   - "all" / "": 不过滤状态
func (r *FileCodeRepository) GetUserSharesWithFilter(ctx context.Context, userID uint, filter UserShareFilter) ([]*model.FileCode, int64, error) {
	page, pageSize := clampPage(filter.Page, filter.PageSize, 100)

	now := time.Now()
	q := r.db().WithContext(ctx).Model(&model.FileCode{}).Where("user_id = ?", userID)

	switch filter.Status {
	case "deleted":
		// 只看已删除
		q = q.Unscoped().Where("deleted_at IS NOT NULL")
	case "active":
		q = q.Where("(expired_at IS NULL OR expired_at > ?) AND (expired_count <> 0)", now)
	case "expired":
		q = q.Where("((expired_at IS NOT NULL AND expired_at <= ?) OR expired_count = 0)")
	case "text":
		q = q.Where("text <> ''")
	case "file":
		q = q.Where("(text = '' OR text IS NULL)")
	}

	if filter.Search != "" {
		like := "%" + filter.Search + "%"
		q = q.Where("code LIKE ? OR prefix LIKE ? OR suffix LIKE ? OR uuid_file_name LIKE ?",
			like, like, like, like)
	}

	return paginate[model.FileCode](q.Order("created_at DESC"), page, pageSize)
}

// BatchSoftDeleteByCodes 按 code 列表软删除（限定 userID 防止越权）
// 返回 (受影响行数, error)
func (r *FileCodeRepository) BatchSoftDeleteByCodes(ctx context.Context, userID uint, codes []string) (int, error) {
	if len(codes) == 0 {
		return 0, nil
	}
	res := r.db().WithContext(ctx).Model(&model.FileCode{}).
		Where("user_id = ? AND code IN ?", userID, codes).
		Update("deleted_at", time.Now())
	if res.Error != nil {
		return 0, res.Error
	}
	return int(res.RowsAffected), nil
}

// BatchExtendByCodes 批量延期
// newExpireAt 为 nil 表示永久（清空 expired_at 字段）
func (r *FileCodeRepository) BatchExtendByCodes(ctx context.Context, userID uint, codes []string, newExpireAt *time.Time) (int, error) {
	if len(codes) == 0 {
		return 0, nil
	}
	updates := map[string]interface{}{}
	if newExpireAt == nil {
		updates["expired_at"] = nil
		updates["expired_count"] = -1
	} else {
		updates["expired_at"] = *newExpireAt
		// 次数设为 -1（无限）让延期后能继续取
		updates["expired_count"] = -1
	}
	res := r.db().WithContext(ctx).Model(&model.FileCode{}).
		Where("user_id = ? AND code IN ?", userID, codes).
		Updates(updates)
	if res.Error != nil {
		return 0, res.Error
	}
	return int(res.RowsAffected), nil
}

// RestoreByCode 恢复软删除的分享（仅 owner）
func (r *FileCodeRepository) RestoreByCode(ctx context.Context, userID uint, code string) error {
	return r.db().WithContext(ctx).Unscoped().Model(&model.FileCode{}).
		Where("user_id = ? AND code = ? AND deleted_at IS NOT NULL", userID, code).
		Update("deleted_at", nil).Error
}

// HardDeleteByCode 永久删除（仅 owner，已软删除的）。返回实际删除行数：
// 0 = 该分享不在回收站（未软删或不存在）。调用方必须对 0 显式报错——
// 此前 0 行删除也返回 nil，直接 hard 活跃分享表现为 200 静默 no-op，
// 运维/集成误判已清理（v0.11.1 修复）。
func (r *FileCodeRepository) HardDeleteByCode(ctx context.Context, userID uint, code string) (int64, error) {
	res := r.db().WithContext(ctx).Unscoped().Model(&model.FileCode{}).
		Where("user_id = ? AND code = ? AND deleted_at IS NOT NULL", userID, code).
		Delete(&model.FileCode{})
	return res.RowsAffected, res.Error
}

// DecrementExpiredCount 原子扣减剩余次数。
// ExpiredCount 语义：-1=无限(只 +used_count), 0=已耗尽(拒绝), >0=剩余(扣减)
// 返回 ok=true 表示扣减成功；ok=false 表示已耗尽（未扣减）。
// 用单条 UPDATE 的 WHERE 条件保证原子性，避免并发超卖。
func (r *FileCodeRepository) DecrementExpiredCount(ctx context.Context, code string) (bool, error) {
	res := r.db().WithContext(ctx).Model(&model.FileCode{}).
		Where("code = ? AND (expired_count = -1 OR expired_count > 0)", code).
		UpdateColumns(map[string]interface{}{
			"expired_count": gorm.Expr("CASE WHEN expired_count > 0 THEN expired_count - 1 ELSE expired_count END"),
			"used_count":    gorm.Expr("used_count + 1"),
		})
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// UpdateViewer 记录取件人信息（IP + 时间 + 累计 +1）
func (r *FileCodeRepository) UpdateViewer(ctx context.Context, code, viewerIP string) error {
	now := time.Now()
	// 用 SQL 原子自增 viewer_count
	return r.db().WithContext(ctx).Model(&model.FileCode{}).
		Where("code = ?", code).
		Updates(map[string]interface{}{
			"viewer_ip":    viewerIP,
			"viewer_at":    now,
			"viewer_count": gorm.Expr("viewer_count + 1"),
		}).Error
}

// ==================== 管理端增强（批量操作 / 富统计） ====================

// BatchDeleteByIDs 批量删除（软删），返回受影响行数
func (r *FileCodeRepository) BatchDeleteByIDs(ctx context.Context, ids []uint) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	res := r.db().WithContext(ctx).Where("id IN ?", ids).Delete(&model.FileCode{})
	return int(res.RowsAffected), res.Error
}

// UpdateExpireByID 管理员改单条过期时间/剩余次数
func (r *FileCodeRepository) UpdateExpireByID(ctx context.Context, id uint, expireAt *time.Time, expiredCount *int) error {
	updates := map[string]interface{}{}
	if expireAt != nil {
		updates["expired_at"] = *expireAt
	}
	if expiredCount != nil {
		updates["expired_count"] = *expiredCount
	}
	if len(updates) == 0 {
		return nil
	}
	return r.db().WithContext(ctx).Model(&model.FileCode{}).Where("id = ?", id).Updates(updates).Error
}

// BatchExtendByIDsAdmin 管理员批量延期（不限 owner）
func (r *FileCodeRepository) BatchExtendByIDsAdmin(ctx context.Context, ids []uint, newExpireAt time.Time) (int, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	res := r.db().WithContext(ctx).Model(&model.FileCode{}).
		Where("id IN ?", ids).
		Updates(map[string]interface{}{"expired_at": newExpireAt, "expired_count": -1})
	return int(res.RowsAffected), res.Error
}

// SuffixStat 文件后缀统计项
type SuffixStat struct {
	Suffix string `json:"suffix"`
	Count  int64  `json:"count"`
}

// TopSuffixes 上传文件后缀 TOP N（Dashboard 文件类型分布）
func (r *FileCodeRepository) TopSuffixes(ctx context.Context, limit int) ([]*SuffixStat, error) {
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	var rows []*SuffixStat
	err := r.db().WithContext(ctx).Model(&model.FileCode{}).
		Select("suffix, COUNT(*) AS count").
		Where("suffix <> ''").
		Group("suffix").
		Order("count DESC").
		Limit(limit).
		Scan(&rows).Error
	return rows, err
}

// CountCreatedBetween 统计 [from, to) 创建数（昨日对比等）
func (r *FileCodeRepository) CountCreatedBetween(ctx context.Context, from, to time.Time) (int64, error) {
	var count int64
	err := r.db().WithContext(ctx).Model(&model.FileCode{}).
		Where("created_at >= ? AND created_at < ?", from, to).
		Count(&count).Error
	return count, err
}

// TrendByDay 按天统计上传数（Dashboard 趋势序列，from 起含当天）
func (r *FileCodeRepository) TrendByDay(ctx context.Context, from time.Time) ([]DayCount, error) {
	var rows []DayCount
	err := r.db().WithContext(ctx).Model(&model.FileCode{}).
		Select("DATE(created_at) AS date, COUNT(*) AS count").
		Where("created_at >= ?", from).
		Group("DATE(created_at)").Order("DATE(created_at)").
		Scan(&rows).Error
	return rows, err
}

// SumUsedCount 累计取件/下载次数
func (r *FileCodeRepository) SumUsedCount(ctx context.Context) (int64, error) {
	var total int64
	err := r.db().WithContext(ctx).Model(&model.FileCode{}).
		Select("COALESCE(SUM(used_count), 0)").Scan(&total).Error
	return total, err
}

// CountByUploadType 按上传类型统计（anonymous/authenticated/presign_*）
func (r *FileCodeRepository) CountByUploadType(ctx context.Context, uploadType string) (int64, error) {
	var count int64
	err := r.db().WithContext(ctx).Model(&model.FileCode{}).
		Where("upload_type = ?", uploadType).Count(&count).Error
	return count, err
}

// CountExpired 统计已过期文件数
func (r *FileCodeRepository) CountExpired(ctx context.Context) (int64, error) {
	var count int64
	err := r.db().WithContext(ctx).Model(&model.FileCode{}).
		Where("(expired_at IS NOT NULL AND expired_at < ?) OR expired_count = 0", time.Now()).
		Count(&count).Error
	return count, err
}

// ==================== 管理端治理（2026-10-03）：组合过滤 + 状态机 ====================

// ListWithFilter 管理端文件列表：组合过滤 + 分页（此前仅 keyword 模糊匹配，
// 无法按上传者/IP/类型/大小/时间/状态定位滥用资源）。
func (r *FileCodeRepository) ListWithFilter(ctx context.Context, q model.FileCodeQuery) ([]*model.FileCode, int64, error) {
	page, pageSize := clampPage(q.Page, q.PageSize, MaxPageSize)

	query := r.db().WithContext(ctx).Model(&model.FileCode{})

	if q.Keyword != "" {
		like := "%" + q.Keyword + "%"
		query = query.Where("code LIKE ? OR prefix LIKE ? OR suffix LIKE ? OR uuid_file_name LIKE ? OR text LIKE ?",
			like, like, like, like, like)
	}
	if q.UserID != nil {
		query = query.Where("user_id = ?", *q.UserID)
	}
	if q.UploadType != "" {
		query = query.Where("upload_type = ?", q.UploadType)
	}
	if q.OwnerIP != "" {
		query = query.Where("owner_ip = ?", q.OwnerIP)
	}
	if q.Status != "" {
		query = query.Where("status = ?", q.Status)
	}
	if q.MinSize != nil {
		query = query.Where("size >= ?", *q.MinSize)
	}
	if q.MaxSize != nil {
		query = query.Where("size <= ?", *q.MaxSize)
	}
	if q.CreatedAfter != nil {
		query = query.Where("created_at >= ?", *q.CreatedAfter)
	}
	if q.CreatedBefore != nil {
		query = query.Where("created_at <= ?", *q.CreatedBefore)
	}
	if q.Expired != nil {
		now := time.Now()
		if *q.Expired {
			// 注意外层括号：缺了会让 OR 逃逸出 AND 链，绕过 status/ip 等全部过滤
			query = query.Where("((expired_at IS NOT NULL AND expired_at < ?) OR expired_count = 0)", now)
		} else {
			query = query.Where("((expired_at IS NULL OR expired_at >= ?) AND expired_count <> 0)", now)
		}
	}

	return paginate[model.FileCode](query.Order("created_at DESC"), page, pageSize)
}

// UpdateStatusByIDs 批量更新管控状态（单个/批量禁用、恢复共用）。
// status 取值经 model.ValidShareStatus 白名单校验；返回受影响行数。
func (r *FileCodeRepository) UpdateStatusByIDs(ctx context.Context, ids []uint, status string) (int64, error) {
	if len(ids) == 0 {
		return 0, nil
	}
	if !model.ValidShareStatus(status) {
		return 0, fmt.Errorf("非法的分享状态: %s", status)
	}
	res := r.db().WithContext(ctx).Model(&model.FileCode{}).
		Where("id IN ?", ids).
		Update("status", status)
	return res.RowsAffected, res.Error
}

// DeleteByIDTx 事务内软删单条分享主记录（原子性由事务保证；
// app 层经此删除，不再直接持有 gorm 会话）。
func (r *FileCodeRepository) DeleteByIDTx(ctx context.Context, id uint) error {
	return r.db().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return tx.Delete(&model.FileCode{}, id).Error
	})
}

// ListAllIncludingDeleted 全量拉取分享记录（含软删）——物理文件对账的引用集：
// 可恢复（软删）分享的物理文件不算孤儿。
func (r *FileCodeRepository) ListAllIncludingDeleted(ctx context.Context) ([]*model.FileCode, error) {
	var rows []*model.FileCode
	if err := r.db().WithContext(ctx).Unscoped().
		Model(&model.FileCode{}).
		Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}
