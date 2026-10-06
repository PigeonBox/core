package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/filescodebox/core/conf"
	"github.com/filescodebox/core/pkg/auth"
	"github.com/filescodebox/core/pkg/logger"
	"github.com/filescodebox/core/pkg/middleware"
	"github.com/filescodebox/core/pkg/security"
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

	// User 用户设置（注册开关/上传限制/存储配额/会话时长）。
	// nil 表示从未在线保存过 → 运行时回退 yaml 全局配置（EffectiveUserSettings）。
	// JSON 键沿用前端 configForm.user 的既有键名，读写端点直传免转换。
	User *UserSettings `json:"user,omitempty"`

	// ===== v0.7.3 扩容的在线设置段（同 User 语义：nil = 未在线设置 → yaml 生效；
	// 非 nil = 保存时整体 overlay 到全局 conf 并热应用，重启由 restoreAdminSettings 恢复）=====
	// 外观（背景图/主题色/管理入口可见性）
	UI *conf.UIConfig `json:"ui,omitempty"`
	// 上传细项（文本上限/扩展名白黑名单/匿名日配额/整文件上限/全局过期上限/过期样式裁剪）
	UploadEx *conf.UploadConfig `json:"upload_ex,omitempty"`
	// 下载（S3 直下/超时/需登录）
	Download *conf.DownloadConfig `json:"download,omitempty"`
	// 通知（Webhook + SMTP；保存经 Reconfigurers 热重建 mailer）
	Notify *conf.NotifyConfig `json:"notify,omitempty"`
	// 本地导入（enabled/roots）
	LocalImport *conf.LocalImportConfig `json:"local_import,omitempty"`
	// OIDC 单点登录
	OIDC *conf.OIDCConfig `json:"oidc,omitempty"`
	// API Key 认证总开关与 per-Key 限流
	APIToken *conf.APITokenConfig `json:"api_token,omitempty"`

	// 新段是否发生变化的标记由「非 nil」即涵盖；保存走 UpdateConfig 既有持久化通道
}

// UserSettings 管理后台"用户配置"标签页的在线设置。
// 语义：作为对应运行时消费点的系统级默认值（用户级覆盖仍然优先）。
type UserSettings struct {
	// AllowUserRegistration 是否开放注册（/user/register 与 /api/config 同源读取）
	AllowUserRegistration bool `json:"allowuserregistration"`
	// UserUploadSize 用户单次上传大小默认上限（字节，0 = 不限）
	UserUploadSize int64 `json:"useruploadsize"`
	// UserStorageQuota 用户存储配额默认值（字节，0 = 不限）
	UserStorageQuota int64 `json:"userstoragequota"`
	// SessionExpiryHours 会话时长（小时，0 = auth 包默认 7 天）
	SessionExpiryHours int `json:"sessionexpiryhours"`
}

// userSettingsFromYAML 无持久化记录时回退 yaml 全局配置（与历史行为一致）。
func userSettingsFromYAML() *UserSettings {
	u := &UserSettings{SessionExpiryHours: 168}
	if cfg := conf.GetGlobalConfig(); cfg != nil {
		u.AllowUserRegistration = cfg.User.AllowUserRegistration
		u.UserUploadSize = cfg.User.UserUploadSize
		u.UserStorageQuota = cfg.User.UserStorageQuota
		if cfg.User.SessionExpiryHours > 0 {
			u.SessionExpiryHours = cfg.User.SessionExpiryHours
		}
	}
	return u
}

type Service struct {
	reconfigurersMu    sync.RWMutex
	reconfigurers      *ReconfigureHooks
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
	federation   FederationNotifier // P2P 联邦口令路由钩子（nil = 非联邦模式）
}

// FederationNotifier P2P 联邦撤销钩子（窄接口，实现在 app/federation；
// bootstrap 注入。admin 删除路径独立于 share 域，须单独挂钩——否则管理端
// 删除的分享在联邦里残留到公告 TTL 过期）。
type FederationNotifier interface {
	ShareDeleted(code string)
}

// SetFederationNotifier 注入联邦撤销钩子（bootstrap 调用；nil = 非联邦）
func (s *Service) SetFederationNotifier(f FederationNotifier) { s.federation = f }

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

	// 3. 联邦撤销公告（未启用为 no-op）
	if s.federation != nil {
		s.federation.ShareDeleted(file.Code)
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

// ============ 管理端增强操作（transport handler 经此下沉，不直连 DAO） ============

// UserListFilter 带筛选的用户列表条件（管理端用户管理）。
type UserListFilter struct {
	Keyword  string
	Status   string
	Role     string
	Page     int
	PageSize int
}

// ListUsersFiltered 带筛选的用户列表（keyword/status/role + 分页）。
func (s *Service) ListUsersFiltered(ctx context.Context, f UserListFilter) ([]*model.UserResp, int64, error) {
	users, total, err := s.userRepo.ListFiltered(ctx, dao.UserFilter{
		Keyword:  f.Keyword,
		Status:   f.Status,
		Role:     f.Role,
		Page:     f.Page,
		PageSize: f.PageSize,
	})
	if err != nil {
		return nil, 0, err
	}
	resps := make([]*model.UserResp, len(users))
	for i, u := range users {
		resps[i] = u.ToResp()
	}
	return resps, total, nil
}

// ResetUserPassword 管理员重置用户密码（写入已哈希口令）。
func (s *Service) ResetUserPassword(ctx context.Context, userID uint, passwordHash string) error {
	if err := s.userRepo.UpdatePasswordHash(ctx, userID, passwordHash); err != nil {
		return err
	}
	// 重置密码 bump 会话纪元：被盗会话即时失效（2026-10-05 审计 P3）
	if berr := s.userRepo.BumpSessionEpoch(ctx, userID); berr == nil {
		middleware.InvalidateIdentity(userID)
	}
	return nil
}

// GetFileByID 按主键取分享记录（管理端详情/下载重定向）。
func (s *Service) GetFileByID(ctx context.Context, id uint) (*model.FileCode, error) {
	return s.fileCodeRepo.GetByID(ctx, id)
}

// GetFileDetail 文件详情：主记录 + 子文件行（旧单文件分享无子行由调用方回退主表合成）。
func (s *Service) GetFileDetail(ctx context.Context, id uint) (*model.FileCode, []*model.FileCodeFile, error) {
	fc, err := s.fileCodeRepo.GetByID(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	children, err := dao.NewFileCodeFileRepository().ListByFileCodeID(ctx, fc.ID)
	if err != nil {
		return fc, nil, err
	}
	return fc, children, nil
}

// ChildFileCounts 批量取子文件数（一次 GROUP BY；失败返回 nil 由调用方降级为 0）。
func (s *Service) ChildFileCounts(ctx context.Context, ids []uint) map[uint]int64 {
	counts, err := dao.NewFileCodeFileRepository().CountByFileCodeIDs(ctx, ids)
	if err != nil {
		return nil
	}
	return counts
}

// UpdateFileExpire 编辑文件（延期 / 改剩余次数）。
func (s *Service) UpdateFileExpire(ctx context.Context, id uint, expireAt *time.Time, expiredCount *int) error {
	return s.fileCodeRepo.UpdateExpireByID(ctx, id, expireAt, expiredCount)
}

// BatchDeleteFiles 批量删除：DB 软删 + 物理文件清理（best-effort，失败不阻断，
// 孤儿由 janitor 对账）。返回受影响行数。
func (s *Service) BatchDeleteFiles(ctx context.Context, ids []uint) (int, error) {
	// 先取口令供联邦撤销（批量接口不回报实际命中，按请求集合撤销；
	// 误撤未删码由联邦客户端心跳补公告自愈）
	codes := make([]string, 0, len(ids))
	if s.federation != nil {
		for _, id := range ids {
			if fc, err := s.fileCodeRepo.GetByID(ctx, id); err == nil {
				codes = append(codes, fc.Code)
			}
		}
	}
	n, err := s.fileCodeRepo.BatchDeleteByIDs(ctx, ids)
	if err == nil && s.federation != nil {
		for _, code := range codes {
			s.federation.ShareDeleted(code)
		}
	}
	return n, err
}

// BatchExtendFiles 管理端跨用户批量延期。
func (s *Service) BatchExtendFiles(ctx context.Context, ids []uint, expireAt time.Time) (int, error) {
	return s.fileCodeRepo.BatchExtendByIDsAdmin(ctx, ids, expireAt)
}

// EnhancedStats Dashboard 富指标（昨日对比/下载总量/类型分布）。
type EnhancedStats struct {
	TodayUploads     int64             `json:"today_uploads"`
	YesterdayUploads int64             `json:"yesterday_uploads"`
	TotalDownloads   int64             `json:"total_downloads"`
	ExpiredFiles     int64             `json:"expired_files"`
	AnonymousFiles   int64             `json:"anonymous_files"`
	PresignFiles     int64             `json:"presign_files"`
	TopSuffixes      []*dao.SuffixStat `json:"top_suffixes"`
	// 站点级存储配额（storage.quota，0=不限）与当前用量（存活 file_codes 合计）
	StorageUsed  int64 `json:"storage_used"`
	StorageQuota int64 `json:"storage_quota"`
}

// EnhancedStats 汇总富指标。单项统计失败按 0 降级（与历史 handler 行为一致）。
func (s *Service) EnhancedStats(ctx context.Context) (*EnhancedStats, error) {
	now := time.Now()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	yesterdayStart := todayStart.Add(-24 * time.Hour)

	stats := &EnhancedStats{}
	stats.TodayUploads, _ = s.fileCodeRepo.CountCreatedBetween(ctx, todayStart, now.Add(time.Minute))
	stats.YesterdayUploads, _ = s.fileCodeRepo.CountCreatedBetween(ctx, yesterdayStart, todayStart)
	stats.TotalDownloads, _ = s.fileCodeRepo.SumUsedCount(ctx)
	stats.ExpiredFiles, _ = s.fileCodeRepo.CountExpired(ctx)
	stats.AnonymousFiles, _ = s.fileCodeRepo.CountByUploadType(ctx, "anonymous")
	presignFiles, _ := s.fileCodeRepo.CountByUploadType(ctx, "presign_anonymous")
	presignAuthFiles, _ := s.fileCodeRepo.CountByUploadType(ctx, "presign_authenticated")
	stats.PresignFiles = presignFiles + presignAuthFiles
	stats.TopSuffixes, _ = s.fileCodeRepo.TopSuffixes(ctx, 10)
	stats.StorageUsed, _ = s.fileCodeRepo.GetTotalSize(ctx)
	if cfg := conf.GetGlobalConfig(); cfg != nil {
		stats.StorageQuota = cfg.Storage.Quota
	}
	return stats, nil
}

// TrendDay 趋势序列单日点。
type TrendDay struct {
	Date      string `json:"date"`
	Uploads   int64  `json:"uploads"`
	Downloads int64  `json:"downloads"`
}

// TrendSeries 连续 N 天的上传/下载趋势（缺失日补 0）。下载日志查询失败不阻断
// （表可能尚无数据/旧库未建）——降级为全 0 序列。
func (s *Service) TrendSeries(ctx context.Context, days int) ([]TrendDay, error) {
	now := time.Now()
	todayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	from := todayStart.AddDate(0, 0, -(days - 1))

	upRows, err := s.fileCodeRepo.TrendByDay(ctx, from)
	if err != nil {
		return nil, err
	}
	dlRows, _ := s.transferLogRepo.TrendByDay(ctx, from, "download")

	upMap := make(map[string]int64, len(upRows))
	for _, r := range upRows {
		upMap[r.Date] = r.Count
	}
	dlMap := make(map[string]int64, len(dlRows))
	for _, r := range dlRows {
		dlMap[r.Date] = r.Count
	}

	series := make([]TrendDay, 0, days)
	for i := 0; i < days; i++ {
		date := from.AddDate(0, 0, i).Format("2006-01-02")
		series = append(series, TrendDay{Date: date, Uploads: upMap[date], Downloads: dlMap[date]})
	}
	return series, nil
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

// Audit 管理端操作审计的统一入口（transport 侧审计写入收口到此，消除与
// service 层的孪生实现；经全站唯一 Default() 实例落库）。
func Audit(ctx context.Context, action, target string, success bool) {
	Default().logAdminOperation(ctx, action, target, success)
}

// defaultSystemConfig 站点配置默认值。
func defaultSystemConfig() *SystemConfig {
	cfg := &SystemConfig{}
	cfg.Base.Name = "FilesCodeBox"
	cfg.Base.Description = "文件分享平台"
	cfg.Base.Port = 8888
	cfg.Storage.Type = "local"
	cfg.Storage.MaxSize = 1024 * 1024 * 1024 // 1GB
	cfg.Transfer.MaxCount = 100
	cfg.Transfer.ExpireDefault = 7 // 7天
	cfg.User = userSettingsFromYAML()
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
	// 历史记录没有 user 段：unmarshal 后是 nil/零值，回退 yaml，
	// 否则升级后会意外把注册当成"关闭"（2026-10-03 假开关接线时补）
	if cfg.User == nil {
		cfg.User = userSettingsFromYAML()
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
	s.applyUserSideEffects(s.config.User)
}

// applyUserSideEffects 用户设置中需要"生效"而非仅存储的部分：
// 会话时长写入 auth 包（新签发 token 即刻采用）。
func (s *Service) applyUserSideEffects(u *UserSettings) {
	if u == nil {
		return
	}
	if u.SessionExpiryHours > 0 {
		auth.SetSessionExpiry(time.Duration(u.SessionExpiryHours) * time.Hour)
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
	if err := validateUserSettings(cfg.User); cfg.User != nil && err != nil {
		return err
	}
	// 出站目标保存期校验（2026-10-05 审计 P3：notify/oidc 段此前零校验，
	// 管理员级 SSRF 至少收紧 scheme/私网语义——与存储端点同一策略开关）
	if cfg.Notify != nil {
		if u := strings.TrimSpace(cfg.Notify.WebhookURL); u != "" {
			if err := security.ValidateEndpointURL(u); err != nil {
				return fmt.Errorf("notify.webhook_url 校验失败: %w", err)
			}
		}
		if h := strings.TrimSpace(cfg.Notify.SMTP.Host); h != "" {
			if err := security.ValidateEndpointHost(h); err != nil {
				return fmt.Errorf("notify.smtp.host 校验失败: %w", err)
			}
		}
	}
	if cfg.OIDC != nil {
		if u := strings.TrimSpace(cfg.OIDC.Issuer); u != "" {
			if err := security.ValidateEndpointURL(u); err != nil {
				return fmt.Errorf("security.oidc.issuer 校验失败: %w", err)
			}
		}
	}
	// local_import roots 白名单校验（2026-10-05 审计 P3：roots=["/"] 时任意
	// 登录用户可经 import-local 导入服务器任意可读文件）
	for _, li := range []*conf.LocalImportConfig{cfg.LocalImport, localImportOfUploadEx(cfg.UploadEx)} {
		if li == nil {
			continue
		}
		for _, root := range li.Roots {
			root = strings.TrimSpace(root)
			if root == "" {
				continue
			}
			if !filepath.IsAbs(root) {
				return fmt.Errorf("local_import.roots 需为绝对路径: %s", root)
			}
			if filepath.Clean(root) == "/" {
				return errors.New("local_import.roots 禁止配置根目录 /")
			}
		}
	}
	return nil
}

// localImportOfUploadEx UploadEx 段内嵌的 local_import（同一配置两种提交形状）
func localImportOfUploadEx(u *conf.UploadConfig) *conf.LocalImportConfig {
	if u == nil {
		return nil
	}
	return &u.LocalImport
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
	// 通用配置保存（thrift 通道）不带 user 段，保留旧值避免被冲掉
	if newConfig.User == nil && s.config != nil {
		newConfig.User = s.config.User
	}
	// v0.7.3 扩容段同规则：请求未携带的段保留旧值（前端按 tab 分批提交）
	if newConfig.UI == nil && s.config != nil {
		newConfig.UI = s.config.UI
	}
	if newConfig.UploadEx == nil && s.config != nil {
		newConfig.UploadEx = s.config.UploadEx
	}
	if newConfig.Download == nil && s.config != nil {
		newConfig.Download = s.config.Download
	}
	if newConfig.Notify == nil && s.config != nil {
		newConfig.Notify = s.config.Notify
	}
	if newConfig.LocalImport == nil && s.config != nil {
		newConfig.LocalImport = s.config.LocalImport
	}
	if newConfig.OIDC == nil && s.config != nil {
		newConfig.OIDC = s.config.OIDC
	}
	if newConfig.APIToken == nil && s.config != nil {
		newConfig.APIToken = s.config.APIToken
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
	s.applyUserSideEffects(newConfig.User)
	s.configMu.Lock()
	s.config = newConfig
	s.configLoaded = true
	// 新段热应用：整体 overlay 到全局 conf（所有 conf.GetGlobalConfig() 读取点即刻生效）
	s.applySystemConfigOverlayLocked(newConfig)
	notifyCfg, oidcCfg := newConfig.Notify, newConfig.OIDC
	s.configMu.Unlock()

	// 组件热重建（bootstrap 注册的钩子；nil 段不触发）
	s.reconfigurersMu.RLock()
	hooks := s.reconfigurers
	s.reconfigurersMu.RUnlock()
	if hooks != nil {
		if notifyCfg != nil && hooks.OnNotifyChanged != nil {
			hooks.OnNotifyChanged(notifyCfg)
		}
		if oidcCfg != nil && hooks.OnOIDCChanged != nil {
			hooks.OnOIDCChanged(oidcCfg)
		}
	}

	s.logAdminOperation(ctx, "config.update", "system config persisted to database", true)
	return nil
}

// ReconfigureHooks 存储域之外的组件热重建钩子（bootstrap 注册；SMTP mailer / OIDC service）。
// 参数为合并后的新段；nil 段表示维持现状。
type ReconfigureHooks struct {
	// OnNotifyChanged Webhook+SMTP 热重建（mailer 按新 SMTP 段重建，host 空=卸载）
	OnNotifyChanged func(n *conf.NotifyConfig)
	// OnOIDCChanged OIDC service 热重建（按新段；Enabled=false 时 handler 走未启用响应）
	OnOIDCChanged func(o *conf.OIDCConfig)
}

func (s *Service) SetReconfigureHooks(h *ReconfigureHooks) {
	s.reconfigurersMu.Lock()
	defer s.reconfigurersMu.Unlock()
	s.reconfigurers = h
}

// applySystemConfigOverlayLocked 把新段 overlay 到全局 conf（调用方持 configMu）。
// 全局 conf 是几乎所有读取点（utils/gate/handler/middleware）的真相源，
// 原地改写即全站热生效；重启后由 restoreAdminSettings 从 system_configs 恢复。
func (s *Service) applySystemConfigOverlayLocked(sc *SystemConfig) {
	g := conf.GetGlobalConfig()
	if g == nil {
		return
	}
	if sc.UI != nil {
		g.UI = *sc.UI
	}
	if sc.UploadEx != nil {
		// 保留既有 local_import（2026-10-05 审计附带 bug：UploadEx 整段覆盖
		// 会把零值的 LocalImport 一并写入——"上传"tab 保存未携带该段时
		// 冲掉已配置 roots）
		prevLocal := g.Upload.LocalImport
		g.Upload = *sc.UploadEx
		if sc.LocalImport == nil && !sc.UploadEx.LocalImport.Enabled && len(sc.UploadEx.LocalImport.Roots) == 0 {
			g.Upload.LocalImport = prevLocal
		}
	}
	if sc.Download != nil {
		g.Download = *sc.Download
	}
	if sc.Notify != nil {
		g.Notify = *sc.Notify
	}
	if sc.LocalImport != nil {
		g.Upload.LocalImport = *sc.LocalImport
	}
	if sc.OIDC != nil {
		g.Security.OIDC = *sc.OIDC
	}
	if sc.APIToken != nil {
		g.Security.APIToken = *sc.APIToken
	}
}

// RestoreAdminSettings 启动时把持久化的新段 overlay 回全局 conf（DB 先于服务启动就绪）。
// 由 bootstrap 在 restoreRuntimeStorage 同相位调用。
func (s *Service) RestoreAdminSettings() {
	s.ensureConfigLoaded(context.Background())
	s.configMu.RLock()
	sc := s.config
	s.configMu.RUnlock()
	if sc == nil {
		return
	}
	s.applySystemConfigOverlayLocked(sc)
}

// UpdateUserSettings 在线更新"用户配置"段：读改写当前配置，仅替换 User，
// 其余段保持不变；写穿 DB 并即时应用副作用（会话时长）。
func (s *Service) UpdateUserSettings(ctx context.Context, u UserSettings) error {
	if err := validateUserSettings(&u); err != nil {
		return err
	}
	s.ensureConfigLoaded(ctx)
	s.configMu.RLock()
	current := s.config
	s.configMu.RUnlock()
	if current == nil {
		current = defaultSystemConfig()
	}
	// 深拷贝当前配置，只替换 User 段
	data, err := json.Marshal(current)
	if err != nil {
		return err
	}
	next := defaultSystemConfig()
	if err := json.Unmarshal(data, next); err != nil {
		return err
	}
	if next.User == nil {
		next.User = &UserSettings{}
	}
	*next.User = u
	return s.UpdateConfig(ctx, next)
}

// validateUserSettings 用户设置合法性（负数一律拒绝；0 语义为"不限/默认"）。
func validateUserSettings(u *UserSettings) error {
	if u == nil {
		return errors.New("user settings is nil")
	}
	if u.UserUploadSize < 0 || u.UserStorageQuota < 0 {
		return errors.New("user upload size / storage quota must be >= 0")
	}
	if u.SessionExpiryHours < 0 || u.SessionExpiryHours > 24*365 {
		return errors.New("session expiry hours out of range (0-8760)")
	}
	return nil
}

// EffectiveUserSettings 生效的用户设置：优先管理后台持久化值，
// 无记录时回退 yaml 全局配置。注册开关、配额默认值、会话时长的
// 运行时消费点统一走这里（修复管理端开关不生效）。
func EffectiveUserSettings(ctx context.Context) UserSettings {
	cfg, err := defaultAdminService().GetConfig(ctx)
	if err != nil || cfg == nil || cfg.User == nil {
		return *userSettingsFromYAML()
	}
	return *cfg.User
}

// defaultAdminService 包级惰性单例（EffectiveUserSettings 等包级入口用；
// DB 未初始化时内部按"回退默认值"降级，不会 panic）。
var (
	defaultSvcOnce sync.Once
	defaultSvc     *Service
)

func defaultAdminService() *Service {
	defaultSvcOnce.Do(func() { defaultSvc = NewService() })
	return defaultSvc
}

// Default 全站共享的 admin Service 实例。
// system_configs 是"单行 JSON 读改写"语义，必须全站单实例：
// 多实例各自缓存内存副本会互相覆盖（用户设置写入后公共配置读旧值的
// 2026-10-03 事故即由此而来）。
func Default() *Service { return defaultAdminService() }

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

	statusChanged := user.Status != status
	user.Status = status
	err = s.userRepo.Update(ctx, user)
	// 封禁/停用 bump 会话纪元：既有 JWT 即时失效（2026-10-05 审计 P2，
	// 此前被封用户 token 可用到自然过期且 refresh 可无限续期）
	if err == nil && statusChanged {
		if berr := s.userRepo.BumpSessionEpoch(ctx, userID); berr == nil {
			middleware.InvalidateIdentity(userID)
		}
	}
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

	// 验证密码（2026-10-05 审计 P3：先比密码再判角色——原顺序下错误信息
	// "权限不足" vs "用户名或密码错误" 可区分"该用户名是管理员"，构成
	// 管理员用户名探测预言机）
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		return "", errors.New("用户名或密码错误")
	}

	// 检查是否为管理员
	if user.Role != "admin" {
		return "", errors.New("权限不足")
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
	childRepo := dao.NewFileCodeFileRepository()
	for _, file := range expiredFiles {
		// 多文件分享子文件一并清理（P0 多文件；失败不阻断 DB 删除）
		if s.storage != nil {
			if children, cerr := childRepo.ListByFileCodeID(ctx, file.ID); cerr == nil {
				for _, c := range children {
					if c.FilePath == "" {
						continue
					}
					if err := s.storage.DeleteFile(ctx, c.FilePath); err != nil {
						logger.Warn("delete child physical file failed during cleanup", zap.String("path", c.FilePath), zap.Error(err))
					}
				}
				if len(children) > 0 {
					_ = childRepo.SoftDeleteByFileCodeIDs(ctx, []uint{file.ID})
				}
			}
		}
		// 删物理文件（失败不阻断 DB 删除）
		// TEMP-DEBUG 2026-10-06: 215 COS 清理不删对象的诊断插桩
		logger.Info("cleanup physical delete diagnostic",
			zap.Bool("storage_nil", s.storage == nil),
			zap.String("raw_file_path", file.FilePath),
			zap.String("uuid", file.UUIDFileName))
		if s.storage != nil && file.FilePath != "" {
			fp := file.GetFilePath()
			logger.Info("cleanup physical delete diagnostic fp",
				zap.String("fp", fp))
			if fp != "" {
				if err := s.storage.DeleteFile(ctx, fp); err != nil {
					logger.Warn("delete physical file failed during cleanup", zap.String("path", fp), zap.Error(err))
				} else {
					logger.Info("cleanup physical delete ok", zap.String("path", fp))
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

	_ = page // 参数已用于说明分页意图；当前实现为占位数据
	_ = pageSize

	return logs, total, nil
}
