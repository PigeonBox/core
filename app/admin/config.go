// config.go 系统配置簇：SystemConfig 读写/懒加载/校验/全局 conf overlay 与运行时存储配置持久化。
package admin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"time"

	"github.com/pigeonbox/core/conf"
	"github.com/pigeonbox/core/pkg/auth"
	"github.com/pigeonbox/core/pkg/logger"
	"github.com/pigeonbox/core/pkg/security"
	"github.com/pigeonbox/core/repo/db/dao"
	"github.com/pigeonbox/core/repo/db/model"
	"go.uber.org/zap"
)

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

// defaultSystemConfig 站点配置默认值。
func defaultSystemConfig() *SystemConfig {
	cfg := &SystemConfig{}
	cfg.Base.Name = "PigeonBox"
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
	// UploadEx 从未在线保存过时回填全局 conf 生效值：管理后台能看到真实
	// 生效的上传准入配置，整段保存也不会把 yaml 值冲成零值
	if s.config.UploadEx == nil {
		if g := conf.GetGlobalConfig(); g != nil {
			ue := g.Upload
			s.config.UploadEx = &ue
		}
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
	// 广播变更（多副本 admin 模式；standalone 下回调为 nil 空转）
	s.notifyConfigPersisted()
	return nil
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
	// 广播变更（多副本 admin 模式；public 副本收到后对存储单例 Reload）
	s.notifyConfigPersisted()
	return nil
}
