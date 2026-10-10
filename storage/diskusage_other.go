//go:build !(linux || darwin)

// diskusage_other.go 非 linux/darwin 平台的 DiskUsage 兜底：返回不支持错误
// （调用方按"容量未知"降级展示，不虚构数字）。
package storage

import "errors"

// ErrDiskUsageUnsupported 当前平台无磁盘容量查询实现。
var ErrDiskUsageUnsupported = errors.New("disk usage query not supported on this platform")

// DiskUsage 非 linux/darwin 平台恒返回 ErrDiskUsageUnsupported。
func DiskUsage(path string) (total, free uint64, err error) {
	return 0, 0, ErrDiskUsageUnsupported
}
