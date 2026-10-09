// service.go Service 骨架与接线：结构体定义/构造/注入 Setter/审计入口/包级单例。
package admin

import (
	"context"
	"sync"

	"github.com/pigeonbox/core/conf"
	"github.com/pigeonbox/core/pkg/logger"
	"github.com/pigeonbox/core/pkg/middleware"
	"github.com/pigeonbox/core/repo/db/dao"
	"github.com/pigeonbox/core/repo/db/model"
	"github.com/pigeonbox/core/storage"
	"go.uber.org/zap"
)

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
	onPersist    func()             // 配置持久化回调（bootstrap 注入；多副本 admin 模式发布变更广播）
}

// FederationNotifier P2P 联邦撤销钩子（窄接口，实现在 app/federation；
// bootstrap 注入。admin 删除路径独立于 share 域，须单独挂钩——否则管理端
// 删除的分享在联邦里残留到公告 TTL 过期）。
type FederationNotifier interface {
	ShareDeleted(code string)
}

// SetFederationNotifier 注入联邦撤销钩子（bootstrap 调用；nil = 非联邦）
func (s *Service) SetFederationNotifier(f FederationNotifier) { s.federation = f }

// SetOnConfigPersisted 注入配置持久化回调（bootstrap 调用）。多副本拆分时
// admin 实例经它发布 Redis 变更广播，public 副本订阅后失效本地缓存并热重建
// （详见 docs/specs/2026-10-06-multi-replica-deployment-modes.md §4）。
func (s *Service) SetOnConfigPersisted(fn func()) {
	s.reconfigurersMu.Lock()
	defer s.reconfigurersMu.Unlock()
	s.onPersist = fn
}

// notifyConfigPersisted 配置已持久化（UpdateConfig / SaveRuntimeStorage 成功路径末尾调用）。
func (s *Service) notifyConfigPersisted() {
	s.reconfigurersMu.RLock()
	fn := s.onPersist
	s.reconfigurersMu.RUnlock()
	if fn != nil {
		fn()
	}
}

// InvalidateRuntimeConfig 丢弃内存配置缓存（仅影响缓存，不写库）。
// 多副本：public 副本收到管理端变更广播后调用，下次 GetConfig 重读 DB——
// 单机 standalone 不调用（保持既有"进程内即真相"语义）。
func (s *Service) InvalidateRuntimeConfig() {
	s.configMu.Lock()
	defer s.configMu.Unlock()
	s.config = nil
	s.configLoaded = false
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
