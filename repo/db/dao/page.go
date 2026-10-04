// page.go 分页样板的单一实现。
//
// 此前 8 个 List 方法各自内联 clamp + Count + Offset/Limit，且存在两套漂移
// 方言（pageSize 超限：回落 20 vs 截断到上限）。统一语义：
//
//	page < 1 → 1；pageSize < 1 → 20；pageSize > max → 截断为 max。
package dao

import "gorm.io/gorm"

// DefaultPageSize / MaxPageSize 分页默认值与上限（各 List 共用）。
const (
	DefaultPageSize = 20
	MaxPageSize     = 200
)

// clampPage 分页参数收敛。
func clampPage(page, pageSize, max int) (int, int) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = DefaultPageSize
	}
	if pageSize > max {
		pageSize = max
	}
	return page, pageSize
}

// paginate 通用 count + 分页 find（q 已含 where 条件；排序由调用方先 Order）。
// 返回 (分页结果, 总数, error)。
func paginate[T any](q *gorm.DB, page, pageSize int) ([]*T, int64, error) {
	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, 0, err
	}
	offset := (page - 1) * pageSize
	var rows []*T
	if err := q.Offset(offset).Limit(pageSize).Find(&rows).Error; err != nil {
		return nil, 0, err
	}
	return rows, total, nil
}
