// filecode_runtime.go 取件运行时簇（存储洞察/永久删除/次数原子扣减/取件人记录）。
package dao

import (
	"context"
	"time"

	"github.com/pigeonbox/core/repo/db/model"
	"gorm.io/gorm"
)

// InsightStats 存储洞察聚合（/admin/storage/insights 用）：
// total/totalSize = 未 purge 全量行（软删对象在 purge 前仍占存储），
// alive = 存活分享数，softDeleted = 回收站行数。
func (r *FileCodeRepository) InsightStats(ctx context.Context) (total, totalSize, alive, softDeleted int64, err error) {
	var a struct {
		Cnt  int64
		Size int64
	}
	if err = r.db().WithContext(ctx).Model(&model.FileCode{}).
		Select("COUNT(*) AS cnt, COALESCE(SUM(size),0) AS size").Scan(&a).Error; err != nil {
		return
	}
	if err = r.db().WithContext(ctx).Model(&model.FileCode{}).
		Where("deleted_at IS NULL").Count(&alive).Error; err != nil {
		return
	}
	err = r.db().WithContext(ctx).Model(&model.FileCode{}).
		Where("deleted_at IS NOT NULL").Count(&softDeleted).Error
	return a.Cnt, a.Size, alive, softDeleted, err
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
