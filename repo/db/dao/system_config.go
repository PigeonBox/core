package dao

import (
	"context"
	"errors"

	"github.com/filescodebox/core/repo/db"
	"github.com/filescodebox/core/repo/db/model"
	"gorm.io/gorm"
)

// ErrDBNotInitialized 全局 DB 未初始化（如纯内存单测、bootstrap 早期阶段）。
var ErrDBNotInitialized = errors.New("dao: database not initialized")

// SystemConfigRepository 系统配置持久化（单行，id=1）。
type SystemConfigRepository struct{}

func NewSystemConfigRepository() *SystemConfigRepository {
	return &SystemConfigRepository{}
}

func (r *SystemConfigRepository) db() *gorm.DB {
	return db.GetDB()
}

// Get 读取配置记录；无记录时返回 (nil, nil)。
func (r *SystemConfigRepository) Get(ctx context.Context) (*model.SystemConfigRecord, error) {
	g := r.db()
	if g == nil {
		return nil, ErrDBNotInitialized
	}
	var rec model.SystemConfigRecord
	err := g.WithContext(ctx).First(&rec).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return nil, nil
		}
		return nil, err
	}
	return &rec, nil
}

// Save 保存配置：已存在则更新首行，否则创建（保证全表至多一行）。
func (r *SystemConfigRepository) Save(ctx context.Context, rec *model.SystemConfigRecord) error {
	g := r.db()
	if g == nil {
		return ErrDBNotInitialized
	}
	existing, err := r.Get(ctx)
	if err != nil {
		return err
	}
	if existing == nil {
		return g.WithContext(ctx).Create(rec).Error
	}
	rec.ID = existing.ID
	rec.CreatedAt = existing.CreatedAt
	return g.WithContext(ctx).Model(existing).UpdateColumn("data", rec.Data).Error
}
