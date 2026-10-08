// page.go 分页样板的单一实现。
//
// 此前 8 个 List 方法各自内联 clamp + Count + Offset/Limit，且存在两套漂移
// 方言（pageSize 超限：回落 20 vs 截断到上限）。统一语义：
//
//	page < 1 → 1；pageSize < 1 → 20；pageSize > max → 截断为 max。
package dao

import (
	"strings"

	"gorm.io/gorm"
)

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

// EscapeLike 转义 LIKE 模式中的通配符（%/ _）并用 ESCAPE 子句声明转义符。
// 用户可控搜索词直接拼 %...% 时，"%%%" 之类的输入会退化为全表扫描级 LIKE
// （成本注入 DoS），且越权语义上可被借做模糊探测。所有用户可控 LIKE 模式
// 必须经此包装：`q.Where("col LIKE ? ESCAPE '\\'", EscapeLike(input))`。
func EscapeLike(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(s)
}

// LikeContains 按 contains 语义构造安全的 LIKE 参数（含 ESCAPE 声明片段）。
// 返回 (pattern, escapeClause)，用法：
//
//	pattern, esc := LikeContains(search)
//	q.Where("col LIKE ? "+esc, pattern)
func LikeContains(s string) (string, string) {
	return "%" + EscapeLike(s) + "%", `ESCAPE '\'`
}
