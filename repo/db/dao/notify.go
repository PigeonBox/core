package dao

import (
	"context"

	"github.com/pigeonbox/core/repo/db"
	"github.com/pigeonbox/core/repo/db/model"
	"gorm.io/gorm"
)

// NotifyRepository 通知数据访问（与其他 service 的 DAO 一致，走全局 db.GetDB()）。
type NotifyRepository struct {
	conn *gorm.DB // nil = 走全局 db.GetDB()（兼容历史无参构造）；非 nil = 注入实例
}

// NewNotifyRepository 构造 repository；可选注入 *gorm.DB（测试隔离/嵌入式场景），缺省走全局 db.GetDB()。注入约定见 doc.go。
func NewNotifyRepository(gormDB ...*gorm.DB) *NotifyRepository {
	r := &NotifyRepository{}
	if len(gormDB) > 0 {
		r.conn = gormDB[0]
	}
	return r
}

func (r *NotifyRepository) db() *gorm.DB {
	if r != nil && r.conn != nil { // r != nil 守卫：兼容历史 nil receiver 直调，见 doc.go
		return r.conn
	}
	return db.GetDB()
}

// Create 创建通知
func (r *NotifyRepository) Create(ctx context.Context, n *model.Notify) error {
	return r.db().WithContext(ctx).Create(n).Error
}

// GetByID 按 ID 查询单条
func (r *NotifyRepository) GetByID(ctx context.Context, id uint) (*model.Notify, error) {
	var n model.Notify
	err := r.db().WithContext(ctx).First(&n, id).Error
	return &n, err
}

// Query 返回带 ctx 的 Notify 查询链，供 service 继续追加 Where/Order/分页。
// 保留链式能力以兼容 notify.go 里的动态查询（List/Active/ListForUser）。
func (r *NotifyRepository) Query(ctx context.Context) *gorm.DB {
	return r.db().WithContext(ctx).Model(&model.Notify{})
}

// UpdateByID 按 ID 更新指定字段，返回受影响行数
func (r *NotifyRepository) UpdateByID(ctx context.Context, id uint, updates map[string]interface{}) (int64, error) {
	res := r.db().WithContext(ctx).Model(&model.Notify{}).Where("id = ?", id).Updates(updates)
	return res.RowsAffected, res.Error
}

// DeleteByID 按 ID 删除，返回受影响行数
func (r *NotifyRepository) DeleteByID(ctx context.Context, id uint) (int64, error) {
	res := r.db().WithContext(ctx).Delete(&model.Notify{}, id)
	return res.RowsAffected, res.Error
}

// UpdateWhere 按条件更新（用于 MarkAllReadForUser 的复杂 where）
func (r *NotifyRepository) UpdateWhere(ctx context.Context, where string, args []interface{}, column string, value interface{}) (int64, error) {
	q := r.db().WithContext(ctx).Model(&model.Notify{})
	if where != "" {
		q = q.Where(where, args...)
	}
	res := q.Update(column, value)
	return res.RowsAffected, res.Error
}

// CountWhere 按条件计数（用于 UnreadCountForUser）
func (r *NotifyRepository) CountWhere(ctx context.Context, where string, args []interface{}) (int64, error) {
	var n int64
	q := r.db().WithContext(ctx).Model(&model.Notify{})
	if where != "" {
		q = q.Where(where, args...)
	}
	err := q.Count(&n).Error
	return n, err
}
