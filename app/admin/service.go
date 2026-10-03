package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"runtime"
	"sync"
	"time"

	"github.com/filescodebox/core/conf"
	"github.com/filescodebox/core/pkg/auth"
	"github.com/filescodebox/core/pkg/logger"
	"github.com/filescodebox/core/pkg/middleware"
	"github.com/filescodebox/core/repo/db/dao"
	"github.com/filescodebox/core/repo/db/model"
	"github.com/filescodebox/core/storage"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
)

type AdminStats struct {
	TotalUsers     int64 `json:"total_users"`
	TotalFiles     int64 `json:"total_files"`
	TotalSize      int64 `json:"total_size"`
	TodayUploads   int64 `json:"today_uploads"`
	TodayDownloads int64 `json:"today_downloads"`
	ExpiredFiles   int64 `json:"expired_files"`
	AnonymousFiles int64 `json:"anonymous_files"`
	UserFiles      int64 `json:"user_files"`
}

type SystemConfig struct {
	Base struct {
		Name        string `json:"name"`
		Description string `json:"description"`
		Port        int    `json:"port"`
	} `json:"base"`

	Storage struct {
		Type    string `json:"type"`
		MaxSize int64  `json:"max_size"`
	} `json:"storage"`

	Transfer struct {
		MaxCount      int `json:"max_count"`
		ExpireDefault int `json:"expire_default"`
	} `json:"transfer"`

	// RuntimeStorage 运行时存储配置（存储域经 RuntimePersister 接口读写，
	// 本域只负责持久化不解释其内容；nil 表示管理端从未在线改过存储后端）。
	RuntimeStorage *conf.StorageConfig `json:"runtime_storage,omitempty"`
}

type Service struct {
	userRepo           *dao.UserRepository
	fileCodeRepo       *dao.FileCodeRepository
	transferLogRepo    *dao.TransferLogRepository
	adminOperationRepo *dao.AdminOperationLogRepository
	chunkRepo          *dao.ChunkRepository
	storage            storage.StorageInterface

	configMu     sync.RWMutex
	config       *SystemConfig
	configLoaded bool // 已尝试过 DB 加载（含无记录的情况），避免每次读都打 DB
	configRepo   *dao.SystemConfigRepository
}

func NewService() *Service {
	return &Service{
		userRepo:           dao.NewUserRepository(),
		fileCodeRepo:       dao.NewFileCodeRepository(),
		transferLogRepo:    dao.NewTransferLogRepository(),
		adminOperationRepo: dao.NewAdminOperationLogRepository(),
		chunkRepo:          dao.NewChunkRepository(),
		configRepo:         dao.NewSystemConfigRepository(),
		// config 保持 nil，由 GetConfig 懒加载默认值 → DB 持久化配置
	}
}

// SetConfig 设置配置（显式注入优先于 DB 持久化配置）
func (s *Service) SetConfig(config *SystemConfig) {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	s.config = config
	s.configLoaded = true
}

// SetStorage 注入存储服务（用于过期清理删物理文件）
func (s *Service) SetStorage(st storage.StorageInterface) {
	s.storage = st
}

// GetStats 获取管理员统计信息
func (s *Service) GetStats(ctx context.Context) (*AdminStats, error) {
	stats := &AdminStats{}

	// 获取文件统计
	totalFiles, err := s.fileCodeRepo.Count(ctx)
	if err == nil {
		stats.TotalFiles = totalFiles
	}

	totalSize, err := s.fileCodeRepo.GetTotalSize(ctx)
	if err == nil {
		stats.TotalSize = totalSize
	}

	todayUploads, err := s.fileCodeRepo.CountTodayUploads(ctx)
	if err == nil {
		stats.TodayUploads = todayUploads
	}

	// 获取用户统计
	users, err := s.userRepo.Count(ctx)
	if err == nil {
		stats.TotalUsers = users
	}

	// 获取今日下载（从 transfer_log 统计）
	// todayDownloads, err := s.transferLogRepo.CountTodayDownloads(ctx)
	// if err == nil {
	// 	stats.TodayDownloads = todayDownloads
	// }

	// 统计匿名上传和用户上传
	// anonymousFiles, _ := s.fileCodeRepo.CountByUploadType(ctx, "anonymous")
	// userFiles, _ := s.fileCodeRepo.CountByUploadType(ctx, "authenticated")
	// stats.AnonymousFiles = anonymousFiles
	// stats.UserFiles = userFiles

	return stats, nil
}

// GetUsers 获取用户列表
func (s *Service) GetUsers(ctx context.Context, page, pageSize int) ([]*model.UserResp, int64, error) {
	users, total, err := s.userRepo.List(ctx, page, pageSize)
	if err != nil {
		return nil, 0, err
	}

	resps := make([]*model.UserResp, len(users))
	for i, user := range users {
		resps[i] = user.ToResp()
	}

	return resps, total, nil
}

// DeleteUser 删除用户（级联软删其全部分享记录；此前"先删用户文件"逻辑被注释）
func (s *Service) DeleteUser(ctx context.Context, userID uint) error {
	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return err
	}

	// 1. 级联软删用户的分享记录（不删物理文件——管理员可从回收站追溯）
	if err := s.fileCodeRepo.DeleteByUserID(ctx, userID); err != nil {
		logger.Warn("soft delete user shares failed", zap.Uint("user_id", userID), zap.Error(err))
	}

	// 2. 删除用户记录
	err = s.userRepo.Delete(ctx, userID)
	s.logAdminOperation(ctx, "user.delete",
		fmt.Sprintf("user %d (username=%s) deleted", userID, user.Username), err == nil)
	return err
}

// GetFiles 获取文件列表
func (s *Service) GetFiles(ctx context.Context, page, pageSize int, search string) ([]*model.FileCode, int64, error) {
	return s.fileCodeRepo.List(ctx, page, pageSize, search)
}

// GetFilesFiltered 管理端文件列表组合过滤（治理 2026-10-03：
// keyword/user/upload_type/owner_ip/status/大小/时间/过期，供 /admin/files/filter）。
func (s *Service) GetFilesFiltered(ctx context.Context, q model.FileCodeQuery) ([]*model.FileCode, int64, error) {
	return s.fileCodeRepo.ListWithFilter(ctx, q)
}

// SetFilesStatus 管理员设置分享管控状态（单个/批量禁用、恢复共用）。
// 返回受影响行数；审计按动作分别落账（file.block / file.unblock / file.status）。
func (s *Service) SetFilesStatus(ctx context.Context, ids []uint, status string) (int64, error) {
	n, err := s.fileCodeRepo.UpdateStatusByIDs(ctx, ids, status)
	if err != nil {
		s.logAdminOperation(ctx, "file.status",
			fmt.Sprintf("set %v status=%s failed: %v", ids, status, err), false)
		return 0, err
	}
	action := "file.status"
	switch status {
	case model.StatusBlocked:
		action = "file.block"
	case model.StatusNormal:
		action = "file.unblock"
	}
	s.logAdminOperation(ctx, action,
		fmt.Sprintf("ids=%v status=%s affected=%d", ids, status, n), true)
	return n, nil
}

// DeleteFile 删除文件（DB 记录 + 物理文件；此前物理删除被注释，造成存储泄漏）
func (s *Service) DeleteFile(ctx context.Context, fileID uint) error {
	file, err := s.fileCodeRepo.GetByID(ctx, fileID)
	if err != nil {
		return err
	}

	// 1. 删除数据库记录
	if err := s.fileCodeRepo.Delete(ctx, fileID); err != nil {
		return err
	}

	// 2. 删除物理文件（失败记日志不阻断——DB 已删，孤儿文件由清理任务兜底）
	if s.storage != nil && file.FilePath != "" {
		if fp := file.GetFilePath(); fp != "" {
			if err := s.storage.DeleteFile(ctx, fp); err != nil {
				logger.Warn("admin delete physical file failed", zap.String("path", fp), zap.Error(err))
			}
		}
	}

	s.logAdminOperation(ctx, "file.delete",
		fmt.Sprintf("file %d (code=%s, name=%s) deleted", fileID, file.Code, file.Text), true)
	return nil
}

// GetFileByCode 按取件码获取文件
func (s *Service) GetFileByCode(ctx context.Context, code string) (*model.FileCode, error) {
	return s.fileCodeRepo.GetByCode(ctx, code)
}

// GetTransferLogs 获取传输日志
func (s *Service) GetTransferLogs(ctx context.Context, query model.TransferLogQuery) ([]*model.TransferLog, int64, error) {
	return s.transferLogRepo.List(ctx, query)
}

// CleanupExpiredFiles 清理过期文件
func (s *Service) CleanupExpiredFiles(ctx context.Context) (int, error) {
	// 获取过期文件
	expiredFiles, err := s.fileCodeRepo.GetExpiredFiles(ctx)
	if err != nil {
		return 0, err
	}

	// 删除过期文件
	deletedCount, err := s.fileCodeRepo.DeleteExpiredFiles(ctx, expiredFiles)
	if err != nil {
		return 0, err
	}

	s.logAdminOperation(ctx, "maintenance.clean_expired_files",
		fmt.Sprintf("cleaned %d expired files", deletedCount), err == nil)
	return deletedCount, nil
}

// CleanupIncompleteUploads 清理未完成的上传
func (s *Service) CleanupIncompleteUploads(ctx context.Context, olderThanHours int) (int, error) {
	// 获取未完成的上传
	incompleteUploads, err := s.chunkRepo.GetIncompleteUploads(ctx, time.Duration(olderThanHours)*time.Hour)
	if err != nil {
		return 0, err
	}

	uploadIDs := make([]string, len(incompleteUploads))
	for i, upload := range incompleteUploads {
		uploadIDs[i] = upload.UploadID
	}

	// 删除未完成的上传记录
	deletedCount, err := s.chunkRepo.DeleteChunksByUploadIDs(ctx, uploadIDs)
	if err != nil {
		return 0, err
	}

	s.logAdminOperation(ctx, "maintenance.clean_incomplete_uploads",
		fmt.Sprintf("cleaned %d incomplete uploads (older than %dh)", deletedCount, olderThanHours), err == nil)
	return deletedCount, nil
}

// logAdminOperation 写入管理操作审计日志（P0 修复：模型/DAO 早已有之，
// 此前从未接线——后台所有变更操作零审计）。审计失败只记运行日志，不阻断业务。
// 操作者从 hertz 请求上下文提取（AdminMiddleware 写入 username）；
// 定时任务等非请求场景记为 system。
func (s *Service) logAdminOperation(ctx context.Context, action, target string, success bool) {
	actorID, actorName, ip := actorFromCtx(ctx)
	entry := &model.AdminOperationLog{
		Action:    action,
		Target:    target,
		Success:   success,
		ActorID:   actorID,
		ActorName: actorName,
		IP:        ip,
	}
	if err := s.adminOperationRepo.Create(ctx, entry); err != nil {
		logger.Warn("admin audit log write failed", zap.String("action", action), zap.Error(err))
	}
}

// actorFromCtx 从请求上下文提取操作者（AuthMiddleware 已将身份写入 ctx value；
// 定时任务等非请求场景记为 system）。
func actorFromCtx(ctx context.Context) (*uint, string, string) {
	var id *uint
	name := "system"
	if uid, ok := middleware.UserIDFromContext(ctx); ok {
		id = &uid
	}
	if n := middleware.UsernameFromContext(ctx); n != "" {
		name = n
	}
	ip := middleware.ClientIPFromContext(ctx)
	return id, name, ip
}

// defaultSystemConfig 站点配置默认值。
func defaultSystemConfig() *SystemConfig {
	cfg := &SystemConfig{}
	cfg.Base.Name = "FileCodeBox"
	cfg.Base.Description = "文件分享平台"
	cfg.Base.Port = 8888
	cfg.Storage.Type = "local"
	cfg.Storage.MaxSize = 1024 * 1024 * 1024 // 1GB
	cfg.Transfer.MaxCount = 100
	cfg.Transfer.ExpireDefault = 7 // 7天
	return cfg
}

// loadPersistedConfig 从 DB 读取持久化配置（单行 system_configs）。
// 无记录返回 (nil, nil)；DB 不可用/记录损坏仅告警并回退默认值——
// 配置读取失败不应阻断业务启动。
func (s *Service) loadPersistedConfig(ctx context.Context) (*SystemConfig, error) {
	rec, err := s.configRepo.Get(ctx)
	if err != nil {
		if errors.Is(err, dao.ErrDBNotInitialized) {
			return nil, nil
		}
		return nil, err
	}
	if rec == nil {
		return nil, nil
	}
	cfg := defaultSystemConfig()
	if err := json.Unmarshal([]byte(rec.Data), cfg); err != nil {
		logger.Error("system config record corrupted, fallback to defaults",
			zap.String("data", rec.Data), zap.Error(err))
		return nil, err
	}
	return cfg, nil
}

// ensureConfigLoaded 懒加载：首次访问时用 DB 持久化配置覆盖默认值。
func (s *Service) ensureConfigLoaded(ctx context.Context) {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	if s.config == nil {
		s.config = defaultSystemConfig()
	}
	if s.configLoaded {
		return
	}
	s.configLoaded = true
	persisted, err := s.loadPersistedConfig(ctx)
	switch {
	case err != nil:
		logger.Error("failed to load persisted system config, using defaults", zap.Error(err))
	case persisted != nil:
		s.config = persisted
		logger.Info("system config loaded from database",
			zap.String("site_name", persisted.Base.Name))
	}
}

// GetConfig 获取系统配置（首次调用会从 DB 加载管理后台保存的配置）
func (s *Service) GetConfig(ctx context.Context) (*SystemConfig, error) {
	s.ensureConfigLoaded(ctx)
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	return s.config, nil
}

// validateSystemConfig 基础有效性校验（写库前的最后一道防线）。
func validateSystemConfig(cfg *SystemConfig) error {
	if cfg == nil {
		return errors.New("config is nil")
	}
	if cfg.Base.Port < 0 || cfg.Base.Port > 65535 {
		return errors.New("base.port out of range (0-65535)")
	}
	if cfg.Storage.MaxSize < 0 {
		return errors.New("storage.max_size must be >= 0")
	}
	if cfg.Transfer.MaxCount < 0 {
		return errors.New("transfer.max_count must be >= 0")
	}
	if cfg.Transfer.ExpireDefault < 0 {
		return errors.New("transfer.expire_default must be >= 0")
	}
	return nil
}

// UpdateConfig 更新系统配置：写穿到 DB（单行），成功后才更新内存。
// 防御性合并：handler 会从请求重建 SystemConfig，请求不含 runtime_storage
// （存储域维护的运行时段），为 nil 时保留旧值，避免保存站点配置时被冲掉。
func (s *Service) UpdateConfig(ctx context.Context, newConfig *SystemConfig) error {
	if err := validateSystemConfig(newConfig); err != nil {
		return err
	}
	s.ensureConfigLoaded(ctx)
	s.configMu.RLock()
	if newConfig.RuntimeStorage == nil && s.config != nil {
		newConfig.RuntimeStorage = s.config.RuntimeStorage
	}
	s.configMu.RUnlock()
	data, err := json.Marshal(newConfig)
	if err != nil {
		return err
	}
	rec := &model.SystemConfigRecord{Data: string(data)}
	if err := s.configRepo.Save(ctx, rec); err != nil {
		if errors.Is(err, dao.ErrDBNotInitialized) {
			// DB 不可用时仅更新内存（纯内存运行形态，如部分单测）
			logger.Warn("system config not persisted: database not initialized")
		} else {
			return err
		}
	}
	s.configMu.Lock()
	defer s.configMu.Unlock()
	s.config = newConfig
	s.configLoaded = true
	s.logAdminOperation(ctx, "config.update", "system config persisted to database", true)
	return nil
}

// LoadRuntimeStorage 读取持久化的运行时存储配置（storage.RuntimePersister 实现）。
// 无记录/未设置返回 nil；DB 不可用返回 nil（与配置加载的降级策略一致）。
func (s *Service) LoadRuntimeStorage(ctx context.Context) *conf.StorageConfig {
	s.ensureConfigLoaded(ctx)
	s.configMu.RLock()
	defer s.configMu.RUnlock()
	if s.config == nil {
		return nil
	}
	return s.config.RuntimeStorage
}

// SaveRuntimeStorage 持久化运行时存储配置（storage.RuntimePersister 实现）。
// 读改写单行 JSON，只更新 runtime_storage 段；DB 不可用时返回错误，
// 由调用方（存储域）决定提示语义——切换已生效，仅持久化失败。
func (s *Service) SaveRuntimeStorage(ctx context.Context, cfg *conf.StorageConfig) error {
	s.ensureConfigLoaded(ctx)
	s.configMu.Lock()
	s.config.RuntimeStorage = cfg
	data, err := json.Marshal(s.config)
	s.configMu.Unlock()
	if err != nil {
		return err
	}
	rec := &model.SystemConfigRecord{Data: string(data)}
	if err := s.configRepo.Save(ctx, rec); err != nil {
		if errors.Is(err, dao.ErrDBNotInitialized) {
			return fmt.Errorf("database not initialized: %w", err)
		}
		return err
	}
	s.logAdminOperation(ctx, "storage.config.persist", "runtime storage config persisted (type="+cfg.Type+")", true)
	return nil
}

// UpdateUserStatus 更新用户状态
func (s *Service) UpdateUserStatus(ctx context.Context, userID uint, status string) error {
	user, err := s.userRepo.GetByID(ctx, userID)
	if err != nil {
		return err
	}

	user.Status = status
	err = s.userRepo.Update(ctx, user)
	s.logAdminOperation(ctx, "user.update_status",
		fmt.Sprintf("user %d (username=%s) status -> %s", userID, user.Username, status), err == nil)
	return err
}

// GenerateTokenForAdmin 生成管理员登录 token
func (s *Service) GenerateTokenForAdmin(ctx context.Context, username, password string) (string, error) {
	// 查找用户
	user, err := s.userRepo.GetByUsername(ctx, username)
	if err != nil {
		return "", errors.New("用户名或密码错误")
	}

	// 检查是否为管理员
	if user.Role != "admin" {
		return "", errors.New("权限不足")
	}

	// 验证密码
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		return "", errors.New("用户名或密码错误")
	}

	// 检查用户状态
	if user.Status != "active" {
		return "", errors.New("用户已被禁用")
	}

	// 生成 token (24小时过期)
	token, err := auth.GenerateToken(user.ID, user.Username, "admin")
	if err != nil {
		return "", errors.New("生成 token 失败")
	}

	// 更新最后登录时间
	now := time.Now()
	user.LastLoginAt = &now
	_ = s.userRepo.Update(ctx, user)

	return token, nil
}

// ==================== 维护工具 API ====================

// CleanExpiredFiles 清理过期文件（DB 记录 + 物理文件）。
// 物理删除失败不阻断 DB 删除（记日志）；DB 删除失败不阻断（下轮重试）。
func (s *Service) CleanExpiredFiles(ctx context.Context) (int64, int64, error) {
	expiredFiles, err := s.fileCodeRepo.GetExpiredFiles(ctx)
	if err != nil {
		return 0, 0, err
	}

	freedSpace := int64(0)
	for _, file := range expiredFiles {
		// 删物理文件（失败不阻断 DB 删除）
		if s.storage != nil && file.FilePath != "" {
			fp := file.GetFilePath()
			if fp != "" {
				if err := s.storage.DeleteFile(ctx, fp); err != nil {
					logger.Warn("delete physical file failed during cleanup", zap.String("path", fp), zap.Error(err))
				}
			}
		}
		freedSpace += file.Size
	}

	count, err := s.fileCodeRepo.DeleteExpiredFiles(ctx, expiredFiles)
	if err != nil {
		return 0, 0, err
	}
	return int64(count), freedSpace, nil
}

// CleanTempFiles 清理临时文件
func (s *Service) CleanTempFiles(ctx context.Context) (int64, int64, error) {
	// 获取24小时前未完成的会话
	incompleteUploads, err := s.chunkRepo.GetIncompleteUploads(ctx, 24*time.Hour)
	if err != nil {
		return 0, 0, err
	}

	uploadIDs := make([]string, 0, len(incompleteUploads))
	for _, upload := range incompleteUploads {
		uploadIDs = append(uploadIDs, upload.UploadID)
	}

	// 删除未完成的上传记录
	deletedCount, err := s.chunkRepo.DeleteChunksByUploadIDs(ctx, uploadIDs)
	if err != nil {
		return 0, 0, err
	}

	return int64(deletedCount), 0, nil
}

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

	_ = page    // 参数已用于说明分页意图；当前实现为占位数据
	_ = pageSize

	return logs, total, nil
}
