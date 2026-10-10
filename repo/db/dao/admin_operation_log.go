package dao

import (
	"context"
	"time"

	"github.com/pigeonbox/core/repo/db"
	"github.com/pigeonbox/core/repo/db/model"
	"gorm.io/gorm"
)

type AdminOperationLogRepository struct {
	conn *gorm.DB // nil = 走全局 db.GetDB()（兼容历史无参构造）；非 nil = 注入实例
}

// NewAdminOperationLogRepository 构造 repository；可选注入 *gorm.DB（测试隔离/嵌入式场景），缺省走全局 db.GetDB()。注入约定见 doc.go。
func NewAdminOperationLogRepository(gormDB ...*gorm.DB) *AdminOperationLogRepository {
	r := &AdminOperationLogRepository{}
	if len(gormDB) > 0 {
		r.conn = gormDB[0]
	}
	return r
}

func (r *AdminOperationLogRepository) db() *gorm.DB {
	if r != nil && r.conn != nil { // r != nil 守卫：兼容历史 nil receiver 直调，见 doc.go
		return r.conn
	}
	return db.GetDB()
}

func (r *AdminOperationLogRepository) Create(ctx context.Context, log *model.AdminOperationLog) error {
	return r.db().WithContext(ctx).Create(log).Error
}

func (r *AdminOperationLogRepository) List(ctx context.Context, query model.AdminOperationLogQuery) ([]*model.AdminOperationLog, int64, error) {
	page, pageSize := clampPage(query.Page, query.PageSize, MaxPageSize)

	dbQuery := r.db().WithContext(ctx).Model(&model.AdminOperationLog{})

	if query.Action != "" {
		dbQuery = dbQuery.Where("action = ?", query.Action)
	}

	if query.Actor != "" {
		actorPattern, esc := LikeContains(query.Actor)
		dbQuery = dbQuery.Where("actor_name LIKE ? "+esc, actorPattern)
	}

	if query.Success != nil {
		dbQuery = dbQuery.Where("success = ?", *query.Success)
	}

	return paginate[model.AdminOperationLog](dbQuery.Order("created_at DESC"), page, pageSize)
}

// DeleteOlderThan 日志保留清理：删除 created_at 早于 cutoff 的行，返回删除行数。
func (r *AdminOperationLogRepository) DeleteOlderThan(ctx context.Context, cutoff time.Time) (int64, error) {
	res := r.db().WithContext(ctx).Where("created_at < ?", cutoff).Delete(&model.AdminOperationLog{})
	return res.RowsAffected, res.Error
}
