// janitor.go 存储对账与日志保留（治理 2026-10-03）。
//
// 此前代码注释多次声称"孤儿文件由清理任务兜底"但该任务并不存在：
// 物理文件在 DB 无引用（写库失败/分片中断/老 bug 遗留）时永不回收。
// 本文件补上两个低频后台任务：
//   - ReconcileOrphans：物理文件 vs DB 引用差集清理。local 后端真删除；
//     远端后端（s3/webdav/多云）只统计报告不删除（远端误删代价不对称，
//     且全量 List 成本高，等真实需求出现再做远端对账）。
//   - CleanupLogs：transfer_log / admin_operation_log 按保留天数清理
//     （此前审计表无限膨胀）。
package admin

import (
	"context"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"

	"github.com/filescodebox/core/pkg/logger"
	"github.com/filescodebox/core/repo/db"
	"github.com/filescodebox/core/repo/db/model"
	"github.com/filescodebox/core/storage"
	"go.uber.org/zap"
)

// Janitor 存储对账 + 日志保留。svc 为 nil 或非 local 时对账自动跳过。
type Janitor struct {
	svc           *storage.StorageService
	retentionDays int // 0 = 永久保留日志
}

// NewJanitor 创建 janitor。
func NewJanitor(svc *storage.StorageService, retentionDays int) *Janitor {
	return &Janitor{svc: svc, retentionDays: retentionDays}
}

// ReconcileOrphans 物理文件对账。返回 (扫描文件数, 清理文件数, error)。
func (j *Janitor) ReconcileOrphans(ctx context.Context) (int, int, error) {
	if j.svc == nil {
		return 0, 0, fmt.Errorf("存储实例未就绪")
	}
	if j.svc.EffectiveType() != storage.StorageTypeLocal {
		logger.Info("orphan reconcile skipped: non-local backend (report-only policy)")
		return 0, 0, nil
	}
	root := j.svc.DataPath()
	if root == "" {
		return 0, 0, fmt.Errorf("存储根目录未配置")
	}

	// 1. 收集 DB 引用集（含软删记录——可恢复的分享其物理文件不算孤儿）
	var rows []*model.FileCode
	if err := db.GetDB().WithContext(ctx).Unscoped().
		Model(&model.FileCode{}).
		Find(&rows).Error; err != nil {
		return 0, 0, err
	}
	referenced := make(map[string]bool, len(rows))
	for _, r := range rows {
		if p := r.GetFilePath(); p != "" {
			referenced[filepath.ToSlash(filepath.Clean(p))] = true
		}
	}

	// 2. 收集分片会话集合
	var uploadIDs []string
	if err := db.GetDB().WithContext(ctx).Unscoped().
		Model(&model.UploadChunk{}).Distinct().Pluck("upload_id", &uploadIDs).Error; err != nil {
		return 0, 0, err
	}
	activeChunks := make(map[string]bool, len(uploadIDs))
	for _, id := range uploadIDs {
		activeChunks[id] = true
	}

	// 3. 遍历物理目录（只看 uploads/ 与 chunks/ 两个受管子树）
	scanned, removed := 0, 0
	cleanFile := func(path string) {
		scanned++
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return
		}
		rel = filepath.ToSlash(filepath.Clean(rel))
		if referenced[rel] {
			return
		}
		if err := j.svc.DeleteFile(ctx, rel); err != nil {
			logger.Warn("orphan file delete failed", zap.String("path", rel), zap.Error(err))
			return
		}
		removed++
	}

	uploadsDir := filepath.Join(root, "uploads")
	if entries, err := os.Stat(uploadsDir); err == nil && entries.IsDir() {
		_ = filepath.WalkDir(uploadsDir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return nil
			}
			cleanFile(path)
			return nil
		})
	}

	// 分片目录：chunks/<uploadID> 整目录粒度对账
	chunksDir := filepath.Join(root, "chunks")
	if entries, err := os.ReadDir(chunksDir); err == nil {
		for _, e := range entries {
			if !e.IsDir() || activeChunks[e.Name()] {
				continue
			}
			dir := filepath.Join(chunksDir, e.Name())
			if err := os.RemoveAll(dir); err != nil {
				logger.Warn("orphan chunk dir delete failed", zap.String("dir", dir), zap.Error(err))
				continue
			}
			removed++
		}
	}

	if removed > 0 {
		logger.Info("orphan reconcile done",
			zap.Int("scanned", scanned), zap.Int("removed", removed))
	}
	return scanned, removed, nil
}

// CleanupLogs 按保留天数清理传输日志与审计日志。返回删除行数。
func (j *Janitor) CleanupLogs(ctx context.Context) (int64, error) {
	if j.retentionDays <= 0 {
		return 0, nil // 0 = 永久保留
	}
	cutoff := time.Now().AddDate(0, 0, -j.retentionDays)
	var total int64
	for _, m := range []any{&model.TransferLog{}, &model.AdminOperationLog{}} {
		res := db.GetDB().WithContext(ctx).Where("created_at < ?", cutoff).Delete(m)
		if res.Error != nil {
			return total, res.Error
		}
		total += res.RowsAffected
	}
	if total > 0 {
		logger.Info("log retention cleanup done",
			zap.Int64("deleted", total), zap.Int("retention_days", j.retentionDays))
	}
	return total, nil
}
