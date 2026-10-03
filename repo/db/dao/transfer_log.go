package dao

import (
	"context"
	"time"

	"github.com/filescodebox/core/repo/db"
	"github.com/filescodebox/core/repo/db/model"
	"gorm.io/gorm"
)

type TransferLogRepository struct {
}

func NewTransferLogRepository() *TransferLogRepository {
	return &TransferLogRepository{}
}

func (r *TransferLogRepository) db() *gorm.DB {
	return db.GetDB()
}

func (r *TransferLogRepository) Create(ctx context.Context, log *model.TransferLog) error {
	return r.db().WithContext(ctx).Create(log).Error
}

// DayCount 按天聚合计数项（趋势统计用）
type DayCount struct {
	Date  string `json:"date"` // YYYY-MM-DD
	Count int64  `json:"count"`
}

// TrendByDay 按天统计传输次数（operation 为空 = 全部类型）。
// DATE() 在 sqlite/mysql/postgres 语义一致，跨库可用。
func (r *TransferLogRepository) TrendByDay(ctx context.Context, from time.Time, operation string) ([]DayCount, error) {
	var rows []DayCount
	q := r.db().WithContext(ctx).Model(&model.TransferLog{}).
		Select("DATE(created_at) AS date, COUNT(*) AS count").
		Where("created_at >= ?", from)
	if operation != "" {
		q = q.Where("operation = ?", operation)
	}
	err := q.Group("DATE(created_at)").Order("DATE(created_at)").Scan(&rows).Error
	return rows, err
}

func (r *TransferLogRepository) List(ctx context.Context, query model.TransferLogQuery) ([]*model.TransferLog, int64, error) {
	page := query.Page
	if page < 1 {
		page = 1
	}

	pageSize := query.PageSize
	if pageSize <= 0 {
		pageSize = 20
	}
	if pageSize > 200 {
		pageSize = 200
	}

	dbQuery := r.db().WithContext(ctx).Model(&model.TransferLog{})

	if query.Operation != "" {
		dbQuery = dbQuery.Where("operation = ?", query.Operation)
	}

	if query.UserID != nil {
		dbQuery = dbQuery.Where("user_id = ?", *query.UserID)
	}

	if query.Search != "" {
		like := "%" + query.Search + "%"
		dbQuery = dbQuery.Where(
			"file_code LIKE ? OR file_name LIKE ? OR username LIKE ? OR ip LIKE ?",
			like, like, like, like,
		)
	}

	var total int64
	if err := dbQuery.Count(&total).Error; err != nil {
		return nil, 0, err
	}

	offset := (page - 1) * pageSize
	var logs []*model.TransferLog
	if err := dbQuery.Order("created_at DESC").Offset(offset).Limit(pageSize).Find(&logs).Error; err != nil {
		return nil, 0, err
	}

	return logs, total, nil
}
