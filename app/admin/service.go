// service.go Service 骨架与接线：结构体定义/构造/注入 Setter/审计入口/包级单例。
//
// 2026-10-10 站点配置中心独立为 app/config 域：本域只保留治理职责
// （用户/文件/统计/维护/认证/审计）与管理面 API 的配置门面委派（config.go）。
package admin

import (
	"context"
	"sync"

	appconfig "github.com/pigeonbox/core/app/config"
	"github.com/pigeonbox/core/pkg/logger"
	"github.com/pigeonbox/core/pkg/middleware"
	"github.com/pigeonbox/core/repo/db/dao"
	"github.com/pigeonbox/core/repo/db/model"
	"github.com/pigeonbox/core/storage"
	"go.uber.org/zap"
)

type Service struct {
	userRepo           *dao.UserRepository
	fileCodeRepo       *dao.FileCodeRepository
	transferLogRepo    *dao.TransferLogRepository
	adminOperationRepo *dao.AdminOperationLogRepository
	chunkRepo          *dao.ChunkRepository
	fileFileRepo       *dao.FileCodeFileRepository // 多文件子表（files/maintenance 消费）
	sysConfigRepo      *dao.SystemConfigRepository // 仅 OptimizeDatabase 表维护用（配置读写归 app/config 域）
	storage            storage.StorageInterface
	federation         FederationNotifier // P2P 联邦口令路由钩子（nil = 非联邦模式）

	// cfg 站点配置中心（app/config 域实例；本域经 config.go 门面委派）。
	// NewService() 持独立实例（测试隔离）；Default() 单例改绑 config.Default()
	// ——system_configs 单行读改写语义要求全站唯一配置实例（2026-10-03 事故）。
	cfg *appconfig.Service
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
		fileFileRepo:       dao.NewFileCodeFileRepository(),
		sysConfigRepo:      dao.NewSystemConfigRepository(),
		cfg:                appconfig.NewService(),
	}
}

// SetStorage 注入存储服务（用于过期清理删物理文件）
func (s *Service) SetStorage(st storage.StorageInterface) {
	s.storage = st
}

// logAdminOperation 写入管理操作审计日志（P0 修复：模型/DAO 早已有之，
// 此前从未接线——后台所有变更操作零审计）。审计失败只记运行日志，不阻断业务。
// 操作者从 hertz 请求上下文提取（middleware.AuditActor 单源，config 域同款）；
// 定时任务等非请求场景记为 system。
func (s *Service) logAdminOperation(ctx context.Context, action, target string, success bool) {
	actorID, actorName, ip := middleware.AuditActor(ctx)
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

// Audit 管理端操作审计的统一入口（transport 侧审计写入收口到此，消除与
// service 层的孪生实现；经全站唯一 Default() 实例落库）。
func Audit(ctx context.Context, action, target string, success bool) {
	Default().logAdminOperation(ctx, action, target, success)
}

// defaultAdminService 包级惰性单例（EffectiveUserSettings 等包级入口用；
// DB 未初始化时内部按"回退默认值"降级，不会 panic）。
var (
	defaultSvcOnce sync.Once
	defaultSvc     *Service
)

func defaultAdminService() *Service {
	defaultSvcOnce.Do(func() {
		defaultSvc = NewService()
		// 全站单例改绑配置中心单例：admin.Default() 与 config.Default() 共享
		// 同一份配置内存缓存与 DB 写通道（单行读改写约束，见 app/config 包注释；
		// 此前配置状态内嵌本域，独立成域后约束随单例绑定延续）。
		defaultSvc.cfg = appconfig.Default()
	})
	return defaultSvc
}

// Default 全站共享的 admin Service 实例（治理面 + 配置门面同一实例；
// 配置单实例约束见 app/config 包注释）。
func Default() *Service { return defaultAdminService() }
