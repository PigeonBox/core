package conf

import "fmt"

// globalConfig 全局配置单例。
// 设计权衡：bootstrap 启动时 SetGlobalConfig 注入，各处用 GetGlobalConfig 读取。
// 采用全局单例避免配置在 service/handler 间层层透传。运行期不可变（无写并发）。
// 未来若迁移到 DI 容器，可改为构造注入。
var globalConfig *AppConfiguration

// AppConfiguration 完整应用配置
type AppConfiguration struct {
	Server        ServerConfig        `mapstructure:"server"`
	Database      DatabaseConfig      `mapstructure:"database"`
	Redis         RedisConfig         `mapstructure:"redis"`
	Log           LogConfig           `mapstructure:"log"`
	App           AppConfig           `mapstructure:"app"`
	User          UserConfig          `mapstructure:"user"`
	Upload        UploadConfig        `mapstructure:"upload"`
	Download      DownloadConfig      `mapstructure:"download"`
	Storage       StorageConfig       `mapstructure:"storage"`
	UI            UIConfig            `mapstructure:"ui"`
	Observability ObservabilityConfig `mapstructure:"observability"`
	Security      SecurityConfig      `mapstructure:"security"`
	RateLimit     RateLimitSettings   `mapstructure:"rate_limit"`
	Notify        NotifyConfig        `mapstructure:"notify"`
	MCP           MCPConfig           `mapstructure:"mcp"`
	Moderation    ModerationConfig    `mapstructure:"moderation"`
	Admin         AdminConfig         `mapstructure:"admin"`
}

// MCPConfig Model Context Protocol server（AI 客户端集成；上游没有的差异化能力）。
// 端点 POST /api/v1/mcp（Streamable HTTP / JSON-RPC 2.0），挂管理员 JWT 认证。
type MCPConfig struct {
	// Enabled 默认 true（认证已强制，无暴露风险）。env: FCB_MCP_ENABLED
	Enabled bool `mapstructure:"enabled"`
}

// AdminConfig 管理端运维配置（治理 2026-10-03）
type AdminConfig struct {
	// LogRetentionDays 审计/传输日志保留天数（0 = 永久）。env: FCB_ADMIN_LOG_RETENTION_DAYS
	LogRetentionDays int `mapstructure:"log_retention_days"`
}

// ModerationConfig 内容审核配置（治理 2026-10-03；默认关闭，词表为空恒放行）
type ModerationConfig struct {
	// Enabled 审核钩子总开关（默认 false；env: FCB_MODERATION_ENABLED）
	Enabled bool `mapstructure:"enabled"`
	// BlockedWords 敏感词表（子串匹配、大小写不敏感；env: FCB_MODERATION_BLOCKED_WORDS，逗号分隔）
	BlockedWords []string `mapstructure:"blocked_words"`
	// BlockAction 命中处置策略：reject（默认，直接拒绝）| pending（建分享后置待审，进管理端队列）
	BlockAction string `mapstructure:"block_action"`
	// ClamAV 文件病毒扫描（clamd INSTREAM；启用后文件侧审核由扫描器接管）
	ClamAV ClamAVConfig `mapstructure:"clamav"`
}

// ClamAVConfig clamd 病毒扫描配置（默认关闭；详见 app/moderation/clamav.go）
type ClamAVConfig struct {
	Enabled        bool   `mapstructure:"enabled"`         // env: FCB_MODERATION_CLAMAV_ENABLED
	Addr           string `mapstructure:"addr"`            // 默认 localhost:3310；env: FCB_MODERATION_CLAMAV_ADDR
	TimeoutSeconds int    `mapstructure:"timeout_seconds"` // 单文件扫描超时，默认 60
	MaxScanBytes   int64  `mapstructure:"max_scan_bytes"`  // 超过跳过扫描，默认 512MB
}

// NotifyConfig 通知配置（站内信 + 外部 Webhook + SMTP 邮件渠道）
type NotifyConfig struct {
	// WebhookURL 外部推送地址：notify.created 事件以 JSON POST 推送（空 = 禁用）。
	// env: FCB_WEBHOOK_URL
	WebhookURL string `mapstructure:"webhook_url"`
	// SMTP 邮件通知（站内信创建后对登记邮箱异步补发；空 host = 禁用）
	SMTP SMTPConfig `mapstructure:"smtp"`
}

// SMTPConfig SMTP 邮件配置（P2；默认禁用）
type SMTPConfig struct {
	Host     string `mapstructure:"host"`     // env: FCB_SMTP_HOST
	Port     int    `mapstructure:"port"`     // 465=隐式 TLS；25/587=STARTTLS；env: FCB_SMTP_PORT
	Username string `mapstructure:"username"` // env: FCB_SMTP_USERNAME
	Password string `mapstructure:"password"` // env: FCB_SMTP_PASSWORD
	From     string `mapstructure:"from"`     // 发件地址，空 = 取 Username；env: FCB_SMTP_FROM
}

// SetGlobalConfig 设置全局配置
func SetGlobalConfig(cfg *AppConfiguration) {
	globalConfig = cfg
}

// GetGlobalConfig 获取全局配置
func GetGlobalConfig() *AppConfiguration {
	return globalConfig
}

// ServerConfig 服务器配置
type ServerConfig struct {
	Host         string `mapstructure:"host"`
	Port         int    `mapstructure:"port"`
	Mode         string `mapstructure:"mode"`     // debug, release, test
	BaseURL      string `mapstructure:"base_url"` // 对外可访问的基础 URL（用于生成分享链接等）
	ReadTimeout  int    `mapstructure:"read_timeout"`
	WriteTimeout int    `mapstructure:"write_timeout"`
}

// DatabaseConfig 数据库配置
type DatabaseConfig struct {
	Driver      string `mapstructure:"driver"` // sqlite, mysql, postgres
	DBName      string `mapstructure:"db_name"`
	Host        string `mapstructure:"host"`
	Port        int    `mapstructure:"port"`
	User        string `mapstructure:"user"`
	Password    string `mapstructure:"password"`
	AutoMigrate bool   `mapstructure:"auto_migrate"` // true=GORM AutoMigrate(开发友好,默认); false=版本化迁移(migrations/)
	Migrate     bool   `mapstructure:"migrate"`      // 启动时执行版本化迁移(企业级)。与 AutoMigrate 可共存：先 migrate 后 AutoMigrate 兜底
}

// RedisConfig Redis配置
type RedisConfig struct {
	Host     string `mapstructure:"host"`
	Port     int    `mapstructure:"port"`
	Password string `mapstructure:"password"`
	DB       int    `mapstructure:"db"`
}

// Addr 返回 Redis 地址
func (c *RedisConfig) Addr() string {
	return fmt.Sprintf("%s:%d", c.Host, c.Port)
}

// PublicBaseURL 对外基础地址：base_url 优先，否则 host:port。
// fallback 供配置未加载（测试）兜底。修复：直传/分片通道此前在
// base_url 未配置时回落到写死的 localhost:12345 常量，反代/容器部署
// 下 API 返回的分享链接不可用。
func (c *ServerConfig) PublicBaseURL(fallback string) string {
	if c != nil {
		if c.BaseURL != "" {
			return c.BaseURL
		}
		if c.Port > 0 {
			return fmt.Sprintf("http://%s:%d", c.Host, c.Port)
		}
	}
	return fallback
}

// LogConfig 日志配置
type LogConfig struct {
	Level      string `mapstructure:"level"`
	Filename   string `mapstructure:"filename"`
	MaxSize    int    `mapstructure:"max_size"`
	MaxBackups int    `mapstructure:"max_backups"`
	MaxAge     int    `mapstructure:"max_age"`
	Compress   bool   `mapstructure:"compress"`
}

// AppConfig 应用配置
type AppConfig struct {
	Name        string `mapstructure:"name"`
	Version     string `mapstructure:"version"`
	Description string `mapstructure:"description"`
	DataPath    string `mapstructure:"datapath"`
	Production  bool   `mapstructure:"production"`
}

// UserConfig 用户配置
type UserConfig struct {
	AllowUserRegistration bool   `mapstructure:"allow_user_registration"`
	UserUploadSize        int64  `mapstructure:"user_upload_size"`
	UserStorageQuota      int64  `mapstructure:"user_storage_quota"`
	SessionExpiryHours    int    `mapstructure:"session_expiry_hours"`
	MaxSessionsPerUser    int    `mapstructure:"max_sessions_per_user"`
	JWTSecret             string `mapstructure:"jwt_secret"`
}

// UploadConfig 上传配置
type UploadConfig struct {
	OpenUpload     bool  `mapstructure:"open_upload"`
	UploadSize     int64 `mapstructure:"upload_size"`
	EnableChunk    bool  `mapstructure:"enable_chunk"`
	ChunkSize    int64 `mapstructure:"chunk_size"`
	RequireLogin bool  `mapstructure:"require_login"`
	// TextMaxBytes 文本分享大小上限（字节）。<=0 时用默认 222KB（对齐上游）。
	TextMaxBytes int64 `mapstructure:"text_max_bytes"`
	// AllowedExtensions 扩展名白名单（如 [".jpg",".png",".pdf"]）。
	// 非空时白名单优先：未命中的扩展名直接拒绝；空 = 黑名单模式。
	AllowedExtensions []string `mapstructure:"allowed_extensions"`
	// BlockedExtensions 扩展名黑名单（如 [".exe",".bat"]）。
	// 非空时覆盖内置默认黑名单；空 = 使用内置默认（可执行文件类）。
	BlockedExtensions []string `mapstructure:"blocked_extensions"`
	// EnableMagicCheck 是否启用魔数校验（默认 true）。
	EnableMagicCheck bool `mapstructure:"enable_magic_check"`
	// AllowedExpireStyles 允许用户选择的过期样式（minute/hour/day/week/month/year/forever）。
	// 空 = 全部允许；管理员可裁剪（对标上游白名单裁剪能力）。
	AllowedExpireStyles []string `mapstructure:"allowed_expire_styles"`
	// MaxSaveSecondsCap 全局过期时间上限（秒），0 = 不限（对标上游 max_save_seconds）。
	MaxSaveSecondsCap int64 `mapstructure:"max_save_seconds_cap"`
	// MaxFileSize 单个文件大小上限（字节），适用于分片/预签名/匿名登记等
	// "整文件"语义的通道；0 = 不限。upload_size 是"单请求体"上限，两者语义不同
	// （回归：分片 init 曾误用 upload_size，48MB 文件被 10MB 单请求限制拦截）。
	MaxFileSize int64 `mapstructure:"max_file_size"`
	// AnonymousDailyCount 匿名上传 per-IP 日配额（次数），0 = 不限。
	// 仅约束匿名请求（登录用户走存储配额）；Redis 可用时多实例共享计数。
	AnonymousDailyCount int64 `mapstructure:"anonymous_daily_count"`
	// AnonymousDailyBytes 匿名上传 per-IP 日配额（字节），0 = 不限。
	AnonymousDailyBytes int64 `mapstructure:"anonymous_daily_bytes"`
	// LocalImport NAS 本地文件免上传导入（P3：服务器本地白名单目录内的文件
	// 直接登记为分享，服务端拷贝入存储；默认关闭）
	LocalImport LocalImportConfig `mapstructure:"local_import"`
}

// LocalImportConfig 本地文件导入配置
type LocalImportConfig struct {
	Enabled bool     `mapstructure:"enabled"` // env: FCB_LOCAL_IMPORT_ENABLED
	Roots   []string `mapstructure:"roots"`   // 允许导入的绝对目录白名单；env: FCB_LOCAL_IMPORT_ROOTS（逗号分隔）
}

// DownloadConfig 下载配置
type DownloadConfig struct {
	DownloadTimeout int  `mapstructure:"download_timeout"`
	RequireLogin             bool `mapstructure:"require_login"`
	// S3DirectDownload s3 直下：存储后端为 s3 且开启时，文件下载 302 到短时效
	// 预签名 GET URL（下载流量不经过服务器）。env: FCB_DOWNLOAD_S3_DIRECT
	S3DirectDownload bool `mapstructure:"s3_direct_download"`
}

// StorageConfig 存储配置
// StorageConfig 存储配置。
// Type 支持：local / s3(含 MinIO) / oss(阿里云) / cos(腾讯云) / bos(百度云) /
// ks3(金山云) / obs(华为云) / webdav。云厂商均走 S3 兼容协议（endpoint 可由
// region 自动推导），各自配置段字段同 CloudStorageConfig。
type StorageConfig struct {
	Type        string `mapstructure:"type"` // local, s3, oss, cos, bos, ks3, obs, webdav
	StoragePath string `mapstructure:"storage_path"`
	// Quota 站点级全局存储配额（字节，0=不限）。统计口径=存活 file_codes 尺寸合计；
	// 全通道统一闸口（直传/分片完成/预签名完成/本地导入/多文件）。env: FCB_STORAGE_QUOTA
	Quota       int64               `mapstructure:"quota"`
	S3          *S3Config           `mapstructure:"s3"`
	WebDAV      *WebDAVConfig       `mapstructure:"webdav"`
	OSS         *CloudStorageConfig `mapstructure:"oss"`
	COS         *CloudStorageConfig `mapstructure:"cos"`
	BOS         *CloudStorageConfig `mapstructure:"bos"`
	KS3         *CloudStorageConfig `mapstructure:"ks3"`
	OBS         *CloudStorageConfig `mapstructure:"obs"`
}

// CloudStorageConfig 云厂商对象存储通用配置（S3 兼容协议）。
// Endpoint 留空时按厂商 + Region 自动推导。
type CloudStorageConfig struct {
	Region    string `mapstructure:"region"` // 厂商地域，如 ap-guangzhou / cn-hangzhou
	Bucket    string `mapstructure:"bucket"`
	AccessKey string `mapstructure:"access_key"`
	SecretKey string `mapstructure:"secret_key"`
	Endpoint  string `mapstructure:"endpoint"`   // 空 = 按厂商+Region 推导
	UseSSL    *bool  `mapstructure:"use_ssl"`    // 缺省 true
	PathStyle *bool  `mapstructure:"path_style"` // 缺省 false（各厂商均为 virtual-host 风格）
}

// S3Config S3 兼容对象存储配置（AWS S3 / 阿里云 OSS / 腾讯云 COS 等）
type S3Config struct {
	Endpoint  string `mapstructure:"endpoint"`
	Region    string `mapstructure:"region"`
	Bucket    string `mapstructure:"bucket"`
	AccessKey string `mapstructure:"access_key"`
	SecretKey string `mapstructure:"secret_key"`
	UseSSL    bool   `mapstructure:"use_ssl"`
	PathStyle bool   `mapstructure:"path_style"`
}

// WebDAVConfig WebDAV 存储配置
type WebDAVConfig struct {
	Endpoint string `mapstructure:"endpoint"`
	Username string `mapstructure:"username"`
	Password string `mapstructure:"password"`
}

// UIConfig 前端 UI 相关配置。
// showAdminAddr：公开页脚是否展示管理后台入口（默认隐藏；/admin 始终可直达，
// 此键仅控制入口可见性）。其余 theme/background 等键待主题系统实装时按新形状回归。
type UIConfig struct {
	RobotsText    string `mapstructure:"robots_text"`
	ShowAdminAddr bool   `mapstructure:"show_admin_addr"`
}

// ObservabilityConfig 可观测性配置（metrics；分布式追踪见路线图，未实现前不暴露配置）
type ObservabilityConfig struct {
	Metrics MetricsConfig `mapstructure:"metrics"`
}

// MetricsConfig Prometheus 指标配置
type MetricsConfig struct {
	Enabled bool   `mapstructure:"enabled"`
	Path    string `mapstructure:"path"` // 指标暴露路径，默认 /metrics
}

// IsProduction 是否生产模式（综合 server.mode 与 app.production 判断）
func (c *AppConfiguration) IsProduction() bool {
	return c.App.Production || c.Server.Mode == "release"
}

// SecurityConfig 安全相关配置（CORS / 可信代理 / 下载令牌 / 防爆破锁定 / SSRF / API Key / OIDC）
type SecurityConfig struct {
	CORS           CORSConfig          `mapstructure:"cors"`
	TrustedProxies []string            `mapstructure:"trusted_proxies"` // 可信代理 CIDR 列表，如 ["10.0.0.0/8","173.245.48.0/20"]
	DownloadToken  DownloadTokenConfig `mapstructure:"download_token"`
	Lockout        LockoutConfig       `mapstructure:"lockout"`
	SSRF           SSRFConfig          `mapstructure:"ssrf"`
	APIToken       APITokenConfig      `mapstructure:"api_token"`
	OIDC           OIDCConfig          `mapstructure:"oidc"`
}

// OIDCConfig OIDC 单点登录配置（P2；默认关闭。启用需 issuer/client_id/client_secret，
// 回调地址 <base_url>/api/v1/user/oidc/callback）
type OIDCConfig struct {
	Enabled          bool   `mapstructure:"enabled"`            // env: FCB_OIDC_ENABLED
	Issuer           string `mapstructure:"issuer"`             // env: FCB_OIDC_ISSUER
	ClientID         string `mapstructure:"client_id"`          // env: FCB_OIDC_CLIENT_ID
	ClientSecret     string `mapstructure:"client_secret"`      // env: FCB_OIDC_CLIENT_SECRET
	Scopes           string `mapstructure:"scopes"`             // 默认 "openid profile email"；env: FCB_OIDC_SCOPES
	FrontendCallback string `mapstructure:"frontend_callback"`  // 默认 /#/oidc/callback
}

// APITokenConfig 用户级 API Key（个人访问令牌，fcb_sk_）。
// Enabled 为认证总开关（env FCB_API_TOKEN_ENABLED）：false 时携带 Key 的请求一律 401。
// PerKeyQPS 为单 Key 独立限流（令牌桶，进程内）：0 = 不限（默认 20，burst 默认 2×QPS）。
type APITokenConfig struct {
	Enabled     bool `mapstructure:"enabled"`       // 默认 true
	PerKeyQPS   int  `mapstructure:"per_key_qps"`   // 默认 20；显式 0 = 不限
	PerKeyBurst int  `mapstructure:"per_key_burst"` // 默认 0 = 2×QPS
}

// DownloadTokenConfig 取件下载令牌（时间窗 HMAC）。
// 启用后 /share/download 必须携带取件查询接口下发的 token（防取件码扫描直下）。
type DownloadTokenConfig struct {
	Enabled         bool `mapstructure:"enabled"`          // 默认 true
	ValiditySeconds int  `mapstructure:"validity_seconds"` // 令牌有效期，默认 1000s（对齐上游）
}

// LockoutConfig 登录/取件失败计数锁定（防爆破，非 QPS 语义）。
type LockoutConfig struct {
	Enabled       bool `mapstructure:"enabled"`        // 默认 true
	MaxAttempts   int  `mapstructure:"max_attempts"`   // 窗口内最大失败次数，默认 10
	WindowSeconds int  `mapstructure:"window_seconds"` // 计数窗口，默认 300s
	LockSeconds   int  `mapstructure:"lock_seconds"`   // 触发后锁定时长，默认 600s
}

// SSRFConfig 存储端点 SSRF 防护。
type SSRFConfig struct {
	// AllowPrivateNetworks 是否允许 s3/webdav 端点指向私网地址。
	// 默认 false（阻止内网探测）；局域网 MinIO/WebDAV（飞牛 NAS）部署需显式开启。
	AllowPrivateNetworks bool `mapstructure:"allow_private_networks"`
}

// RateLimitSettings 限流配置（代码内默认值见 middleware.DefaultRateLimitConfig）。
// Enabled 用指针区分"未配置"（默认开）与"显式 false"。
type RateLimitSettings struct {
	Enabled      *bool `mapstructure:"enabled"`
	GlobalQPS    int   `mapstructure:"global_qps"`
	UploadQPS    int   `mapstructure:"upload_qps"`
	DownloadQPS  int   `mapstructure:"download_qps"`
	LoginQPS     int   `mapstructure:"login_qps"`
	Burst        int   `mapstructure:"burst"`
	BlockSeconds int   `mapstructure:"block_seconds"`
	UseRedis     bool  `mapstructure:"use_redis"` // true=Redis 分布式计数（多实例共享），Redis 不可用自动回退内存
}

// CORSConfig 跨域配置。
//
// 安全说明：当 AllowCredentials=true 时，AllowOrigins 不可为 "*"（浏览器规范），
// 必须显式列出可信来源。若 AllowOrigins 为空且 AllowCredentials=true，
// 中间件会退化为反射 Origin（仅适合开发环境）。
type CORSConfig struct {
	AllowOrigins     []string `mapstructure:"allow_origins"`     // 可信来源列表，env: FCB_CORS_ALLOW_ORIGINS（逗号分隔）
	AllowCredentials bool     `mapstructure:"allow_credentials"` // 是否允许携带凭证
	EnableHSTS       bool     `mapstructure:"enable_hsts"`       // 启用 HSTS（仅 HTTPS 部署）
}
