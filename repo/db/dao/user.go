package dao

import (
	"context"

	"github.com/filescodebox/core/repo/db"
	"github.com/filescodebox/core/repo/db/model"
	"gorm.io/gorm"
)

type UserRepository struct {
}

func NewUserRepository() *UserRepository {
	return &UserRepository{}
}

func (r *UserRepository) db() *gorm.DB {
	return db.GetDB()
}

func (r *UserRepository) Create(ctx context.Context, user *model.User) error {
	return r.db().WithContext(ctx).Create(user).Error
}

func (r *UserRepository) Update(ctx context.Context, user *model.User) error {
	return r.db().WithContext(ctx).Save(user).Error
}

// UpdateColumns 按列原子更新（统计自增等并发场景，替代读改写）
func (r *UserRepository) UpdateColumns(ctx context.Context, id uint, updates map[string]interface{}) error {
	return r.db().WithContext(ctx).Model(&model.User{}).Where("id = ?", id).Updates(updates).Error
}

// UpdatePasswordHash 管理员重置密码（直接写 bcrypt 哈希）
func (r *UserRepository) UpdatePasswordHash(ctx context.Context, id uint, passwordHash string) error {
	return r.db().WithContext(ctx).Model(&model.User{}).Where("id = ?", id).
		Update("password_hash", passwordHash).Error
}

// UserFilter 用户列表筛选（管理端）
type UserFilter struct {
	Keyword  string // 匹配 username/email/nickname
	Status   string // active/inactive/banned；空 = 全部
	Role     string // admin/user；空 = 全部
	Page     int
	PageSize int
}

// ListFiltered 带筛选的用户分页列表（管理端用户管理）
func (r *UserRepository) ListFiltered(ctx context.Context, f UserFilter) ([]*model.User, int64, error) {
	page, pageSize := clampPage(f.Page, f.PageSize, MaxPageSize)
	q := r.db().WithContext(ctx).Model(&model.User{})
	if f.Keyword != "" {
		like := "%" + f.Keyword + "%"
		q = q.Where("username LIKE ? OR email LIKE ? OR nickname LIKE ?", like, like, like)
	}
	if f.Status != "" {
		q = q.Where("status = ?", f.Status)
	}
	if f.Role != "" {
		q = q.Where("role = ?", f.Role)
	}
	return paginate[model.User](q.Order("created_at DESC"), page, pageSize)
}

func (r *UserRepository) Delete(ctx context.Context, id uint) error {
	return r.db().WithContext(ctx).Delete(&model.User{}, id).Error
}

func (r *UserRepository) GetByID(ctx context.Context, id uint) (*model.User, error) {
	var user model.User
	err := r.db().WithContext(ctx).First(&user, id).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func (r *UserRepository) GetByUsername(ctx context.Context, username string) (*model.User, error) {
	var user model.User
	err := r.db().WithContext(ctx).Where("username = ?", username).First(&user).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func (r *UserRepository) GetByEmail(ctx context.Context, email string) (*model.User, error) {
	var user model.User
	err := r.db().WithContext(ctx).Where("email = ?", email).First(&user).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

// GetByOIDCSub 按 OIDC subject 精确匹配（P2 SSO）
func (r *UserRepository) GetByOIDCSub(ctx context.Context, sub string) (*model.User, error) {
	var user model.User
	err := r.db().WithContext(ctx).Where("oidc_sub = ?", sub).First(&user).Error
	if err != nil {
		return nil, err
	}
	return &user, nil
}

func (r *UserRepository) List(ctx context.Context, page, pageSize int) ([]*model.User, int64, error) {
	page, pageSize = clampPage(page, pageSize, MaxPageSize)
	return paginate[model.User](r.db().WithContext(ctx).Model(&model.User{}), page, pageSize)
}

// Count returns the total number of users
func (r *UserRepository) Count(ctx context.Context) (int64, error) {
	var count int64
	err := r.db().WithContext(ctx).Model(&model.User{}).Count(&count).Error
	return count, err
}

// CountAdminUsers returns the total number of admin users
func (r *UserRepository) CountAdminUsers(ctx context.Context) (int64, error) {
	var count int64
	err := r.db().WithContext(ctx).Model(&model.User{}).Where("role = ?", "admin").Count(&count).Error
	return count, err
}
