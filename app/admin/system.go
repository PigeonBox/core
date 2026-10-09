// system.go 系统信息/日志簇：运行时信息/存储状态/系统日志查询。
package admin

import (
	"context"
	"runtime"
	"time"
)

// SystemInfo 系统信息
type SystemInfo struct {
	Version     string `json:"version"`
	OS          string `json:"os"`
	Arch        string `json:"arch"`
	Uptime      string `json:"uptime"`
	Goroutines  int64  `json:"goroutines"`
	MemoryAlloc int64  `json:"memory_alloc"`
	MemoryTotal int64  `json:"memory_total"`
	MemorySys   int64  `json:"memory_sys"`
}

// GetSystemInfo 获取系统信息
func (s *Service) GetSystemInfo(ctx context.Context) (*SystemInfo, error) {
	var mem runtime.MemStats
	runtime.ReadMemStats(&mem)

	uptime := time.Since(time.Now()).Truncate(time.Second)

	return &SystemInfo{
		Version:     "1.0.0",
		OS:          runtime.GOOS,
		Arch:        runtime.GOARCH,
		Uptime:      uptime.String(),
		Goroutines:  int64(runtime.NumGoroutine()),
		MemoryAlloc: int64(mem.Alloc),
		MemoryTotal: int64(mem.TotalAlloc),
		MemorySys:   int64(mem.Sys),
	}, nil
}

// StorageStatus 存储状态
type StorageStatus struct {
	StorageType  string  `json:"storage_type"`
	TotalSpace   int64   `json:"total_space"`
	UsedSpace    int64   `json:"used_space"`
	FreeSpace    int64   `json:"free_space"`
	FileCount    int64   `json:"file_count"`
	UsagePercent float64 `json:"usage_percent"`
}

// GetStorageStatus 获取存储状态
func (s *Service) GetStorageStatus(ctx context.Context) (*StorageStatus, error) {
	// 获取文件总数和总大小
	totalFiles, err := s.fileCodeRepo.Count(ctx)
	if err != nil {
		return nil, err
	}

	totalSize, err := s.fileCodeRepo.GetTotalSize(ctx)
	if err != nil {
		return nil, err
	}

	// 简化实现，假设使用本地存储
	storageType := "local"
	totalSpace := int64(100 * 1024 * 1024 * 1024) // 100GB 默认
	freeSpace := totalSpace - totalSize
	usagePercent := float64(0)
	if totalSpace > 0 {
		usagePercent = (float64(totalSize) / float64(totalSpace)) * 100
	}

	return &StorageStatus{
		StorageType:  storageType,
		TotalSpace:   totalSpace,
		UsedSpace:    totalSize,
		FreeSpace:    freeSpace,
		FileCount:    totalFiles,
		UsagePercent: usagePercent,
	}, nil
}

// LogEntry 日志条目
type LogEntry struct {
	ID        uint64 `json:"id"`
	Level     string `json:"level"`
	Message   string `json:"message"`
	CreatedAt string `json:"created_at"`
	Module    string `json:"module"`
	UserID    string `json:"user_id"`
}

// GetSystemLogs 获取系统日志
func (s *Service) GetSystemLogs(ctx context.Context, level string, page, pageSize int) ([]*LogEntry, int64, error) {
	// 暂时返回模拟数据
	// TODO: 实现真实的日志查询
	logs := []*LogEntry{}
	total := int64(0)

	if level == "" || level == "info" {
		logs = append(logs, &LogEntry{
			ID:        1,
			Level:     "info",
			Message:   "系统启动成功",
			CreatedAt: time.Now().Format("2006-01-02 15:04:05"),
			Module:    "system",
			UserID:    "",
		})
		total = 1
	}

	_ = page // 参数已用于说明分页意图；当前实现为占位数据
	_ = pageSize

	return logs, total, nil
}
