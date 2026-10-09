// filecode_pickup.go 码唯一性检查与 6 位取件码簇（铸造/占用检查/反查/作废）。
package dao

import (
	"context"

	"github.com/pigeonbox/core/repo/db/model"
	"gorm.io/gorm"
)

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

// CheckCodeExistsFolded 大小写折叠占用检查：查询折叠开启时自定义口令创建
// 用它兜底查重，防 ABC/abc 两条并存后在折叠查询下互撞。
func (r *FileCodeRepository) CheckCodeExistsFolded(ctx context.Context, code string, excludeID uint) (bool, error) {
	var existingFile model.FileCode
	err := r.db().WithContext(ctx).
		Where("UPPER(code) = UPPER(?) AND id != ?", code, excludeID).
		First(&existingFile).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// SetPickupCode 铸造 6 位取件码落库（file_codes.pickup_code 列，真相源）。
// 先查占用再更新：唯一索引兜底跨分享撞码，占用返回 ok=false 由调用方换码重试；
// 软删行天然不参与（gorm 作用域），回收站分享的码可被新分享重新生成。
func (r *FileCodeRepository) SetPickupCode(ctx context.Context, code, pickupCode string) (bool, error) {
	taken, err := r.CheckPickupCodeExists(ctx, pickupCode)
	if err != nil || taken {
		return false, err
	}
	res := r.db().WithContext(ctx).Model(&model.FileCode{}).
		Where("code = ?", code).
		UpdateColumn("pickup_code", pickupCode)
	if res.Error != nil {
		return false, res.Error
	}
	return res.RowsAffected > 0, nil
}

// CheckPickupCodeExists 取件码占用检查（存活行内唯一；软删行不占位）。
func (r *FileCodeRepository) CheckPickupCodeExists(ctx context.Context, pickupCode string) (bool, error) {
	var existing model.FileCode
	err := r.db().WithContext(ctx).Where("pickup_code = ?", pickupCode).First(&existing).Error
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// GetByPickupCode 按取件码反查分享（KV 缓存未命中时的 DB 回退路径：
// 内存模式重启丢 KV、永久分享不写 KV，均由此解析）。
func (r *FileCodeRepository) GetByPickupCode(ctx context.Context, pickupCode string) (*model.FileCode, error) {
	var fileCode model.FileCode
	err := r.db().WithContext(ctx).Where("pickup_code = ?", pickupCode).First(&fileCode).Error
	if err != nil {
		return nil, err
	}
	return &fileCode, nil
}

// ClearPickupCode 作废取件码落库侧（Cancel 消费）：KV 映射与 DB 列同步清除。
func (r *FileCodeRepository) ClearPickupCode(ctx context.Context, pickupCode string) error {
	return r.db().WithContext(ctx).Model(&model.FileCode{}).
		Where("pickup_code = ?", pickupCode).
		UpdateColumn("pickup_code", nil).Error
}
