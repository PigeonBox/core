//go:build linux || darwin

// diskusage_unix.go 本地磁盘容量查询（linux/darwin：Statfs）。
package storage

import "syscall"

// DiskUsage 返回 path 所在文件系统的 (总容量, 可用容量) 字节数。
// 可用口径 = Bavail（非特权用户实际可用，不含 root 保留块）。
func DiskUsage(path string) (total, free uint64, err error) {
	var st syscall.Statfs_t
	if err = syscall.Statfs(path, &st); err != nil {
		return 0, 0, err
	}
	return st.Blocks * uint64(st.Bsize), st.Bavail * uint64(st.Bsize), nil
}
