package bootstrap

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/app/server"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/filescodebox/core/conf"
	"github.com/filescodebox/core/gen/router"
	"github.com/filescodebox/core/pkg/auth"
	"github.com/filescodebox/core/pkg/logger"
	"github.com/filescodebox/core/pkg/middleware"
	"github.com/filescodebox/core/pkg/resp"
	securityPkg "github.com/filescodebox/core/pkg/security"
	previewPkg "github.com/filescodebox/core/preview"
	"github.com/filescodebox/core/repo/db"
	"github.com/filescodebox/core/repo/db/model"
	"github.com/filescodebox/core/repo/redis"
	"github.com/filescodebox/core/storage"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/common/expfmt"
	"github.com/spf13/viper"
	"go.uber.org/zap"
	"golang.org/x/crypto/bcrypt"
	"gorm.io/gorm"

	adminApp "github.com/filescodebox/core/app/admin"
	mcpApp "github.com/filescodebox/core/app/mcp"
	notifyAppService "github.com/filescodebox/core/app/notify"
	setupApp "github.com/filescodebox/core/app/setup"
	shareService "github.com/filescodebox/core/app/share"
	storageApp "github.com/filescodebox/core/app/storage"
	userService "github.com/filescodebox/core/app/user"
	adminHandler "github.com/filescodebox/core/gen/handler/admin"
	chunkHandler "github.com/filescodebox/core/gen/handler/chunk"
	notifyHandler "github.com/filescodebox/core/gen/handler/notify"
	presignHandler "github.com/filescodebox/core/gen/handler/presign"
	ratelimitHandler "github.com/filescodebox/core/gen/handler/ratelimit"
	shareHandler "github.com/filescodebox/core/gen/handler/share"
	anonHandler "github.com/filescodebox/core/gen/handler/share_anonymous"
	storageHandler "github.com/filescodebox/core/gen/handler/storage"
	"github.com/filescodebox/core/repo/db/dao"
	customHandler "github.com/filescodebox/core/transport/http/handler"
	customMw "github.com/filescodebox/core/transport/http/middleware"
)

// Config 与 internal/conf 包中的统一配置类型互为别名。
type Config = conf.AppConfiguration

// staticOpts 保存最近一次 Bootstrap 应用的选项(静态服务等闭包读取)。
var staticOpts = defaultOptions()

// CORS 跨域中间件（配置化）。
//
// 安全策略：
//   - 配置了 allow_origins 白名单时，仅放行白名单内的 Origin（生产推荐）
//   - 未配置白名单时，退化为反射 Origin（便于本地开发，等同于宽松模式）
//   - allow_credentials=true 时，绝不返回 "*"，而是精确匹配的 Origin
//
// 同时允许 X-Trace-Id / X-API-Key 等自定义请求头跨域。
// allow_origins 来源：yaml 的 security.cors.allow_origins（数组）或
// 环境变量 FCB_CORS_ALLOW_ORIGINS（逗号分隔，如 "https://a.com,https://b.com"）。
func CORS() app.HandlerFunc {
	allowOrigins := map[string]bool{}
	// 优先从环境变量读取（逗号分隔），兼容 slice 字段在 env 下的传递
	if envOrigins := os.Getenv("FCB_CORS_ALLOW_ORIGINS"); envOrigins != "" {
		for _, o := range strings.Split(envOrigins, ",") {
			if o = strings.TrimSpace(o); o != "" {
				allowOrigins[o] = true
			}
		}
	}
	// 再合并配置文件中的白名单
	for _, o := range config.Security.CORS.AllowOrigins {
		allowOrigins[o] = true
	}
	allowCredentials := config.Security.CORS.AllowCredentials

	return func(ctx context.Context, c *app.RequestContext) {
		origin := string(c.GetHeader("Origin"))

		allowedOrigin := ""
		if origin != "" {
			if allowOrigins[origin] {
				// 白名单精确匹配
				allowedOrigin = origin
			} else if isLocalhostOrigin(origin) {
				// 无白名单或未命中白名单时，允许 localhost 跨域（开发友好）
				allowedOrigin = origin
			}
		}

		if allowedOrigin != "" {
			c.Header("Access-Control-Allow-Origin", allowedOrigin)
			c.Header("Vary", "Origin")
			if allowCredentials {
				// 凭证仅对允许的 origin 生效
				c.Header("Access-Control-Allow-Credentials", "true")
			}
		}

		c.Header("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Requested-With, X-Trace-Id, X-API-Key")
		c.Header("Access-Control-Expose-Headers", "Content-Length, Content-Type, X-Trace-Id")
		c.Header("Access-Control-Max-Age", "86400")

		// 处理预检请求
		if string(c.Method()) == "OPTIONS" {
			c.AbortWithStatus(consts.StatusNoContent)
			return
		}

		c.Next(ctx)
	}
}

// isLocalhostOrigin 判断是否 localhost/127.0.0.1 的任意端口（开发环境跨域放行）。
// 生产环境应通过 FCB_CORS_ALLOW_ORIGINS 显式配置白名单。
func isLocalhostOrigin(origin string) bool {
	return strings.HasPrefix(origin, "http://localhost:") ||
		strings.HasPrefix(origin, "http://127.0.0.1:") ||
		strings.HasPrefix(origin, "https://localhost:") ||
		strings.HasPrefix(origin, "https://127.0.0.1:") ||
		origin == "http://localhost" || origin == "http://127.0.0.1" ||
		origin == "https://localhost" || origin == "https://127.0.0.1"
}

// GetConfig 获取全局配置
func GetConfig() *Config {
	return config
}

// InitConfig 初始化配置。
//
// 配置来源优先级（高 → 低）：
//  1. 环境变量（FCB_ 前缀完整名 / 文档化的短名，见 bindEnvironment）
//  2. 配置文件（yaml，路径由 configPath 或 CONFIG_PATH 决定）
//  3. 代码内默认值（SetDefault）
//
// 这样容器化部署（K8s/Docker）可通过 env 注入敏感配置（jwt_secret、db 密码等），
// 而无需修改镜像内的配置文件，符合 12-factor。
func InitConfig(configPath string) (*Config, error) {
	// 解析最终配置文件路径：参数 > CONFIG_PATH env > 默认
	if configPath == "" {
		configPath = os.Getenv("CONFIG_PATH")
	}
	if configPath == "" {
		configPath = "configs/config.yaml"
	}

	v := viper.New()
	v.SetConfigFile(configPath)
	v.SetConfigType("yaml")

	// 代码内默认值（仅当文件与 env 均未设置时生效）
	setDefaults(v)

	// 绑定环境变量（优先级最高）
	bindEnvironment(v)

	// 读取配置文件（缺失时降级为仅默认值 + env，便于无文件启动）
	if err := v.ReadInConfig(); err != nil {
		log.Printf("Warning: Failed to read config file %s: %v, using defaults + env", configPath, err)
	} else {
		log.Printf("Loaded config from: %s", v.ConfigFileUsed())
	}

	var cfg Config
	if err := v.Unmarshal(&cfg); err != nil {
		return nil, fmt.Errorf("failed to unmarshal config: %w", err)
	}

	// 生产环境敏感配置 fail-fast 校验
	if err := validateSecrets(&cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// setDefaults 设置代码内默认值。
func setDefaults(v *viper.Viper) {
	v.SetDefault("server.host", "0.0.0.0")
	v.SetDefault("server.port", 12345)
	v.SetDefault("server.mode", "debug")
	v.SetDefault("server.base_url", "")
	v.SetDefault("database.driver", "sqlite")
	v.SetDefault("database.db_name", "./data/filecodebox.db")
	v.SetDefault("user.allow_user_registration", true)
	v.SetDefault("user.require_email_verify", false)
	v.SetDefault("observability.metrics.enabled", false)
	v.SetDefault("observability.metrics.path", "/metrics")
	v.SetDefault("observability.tracing.enabled", false)
	// 安全默认（安全加固项，未配置时全开）：
	v.SetDefault("upload.enable_magic_check", true)
	v.SetDefault("security.download_token.enabled", true)
	v.SetDefault("security.download_token.validity_seconds", 1000)
	v.SetDefault("security.lockout.enabled", true)
	v.SetDefault("security.lockout.max_attempts", 10)
	v.SetDefault("security.lockout.window_seconds", 300)
	v.SetDefault("security.lockout.lock_seconds", 600)
	v.SetDefault("rate_limit.enabled", true)
	v.SetDefault("rate_limit.global_qps", 100)
	v.SetDefault("rate_limit.upload_qps", 10)
	v.SetDefault("rate_limit.download_qps", 50)
	v.SetDefault("rate_limit.login_qps", 5)
	v.SetDefault("rate_limit.burst", 20)
	v.SetDefault("rate_limit.block_seconds", 60)
	// MCP server 默认开启（路由挂管理员认证，无暴露风险）
	v.SetDefault("mcp.enabled", true)
}

// envBindings 环境变量 → 配置 key 的映射。
// 同时支持两套命名：
//   - 文档化的短扁平名（PORT / DATABASE_HOST 等，便于运维记忆）
//   - FCB_ 前缀 + 下划线的完整名（FCB_SERVER_PORT，与 mapstructure key 对齐）
var envBindings = map[string][]string{
	// server
	"server.host":          {"FCB_SERVER_HOST", "HOST"},
	"server.port":          {"FCB_SERVER_PORT", "PORT"},
	"server.mode":          {"FCB_SERVER_MODE"},
	"server.base_url":      {"FCB_SERVER_BASE_URL", "BASE_URL"},
	"server.read_timeout":  {"FCB_SERVER_READ_TIMEOUT"},
	"server.write_timeout": {"FCB_SERVER_WRITE_TIMEOUT"},
	// database
	"database.driver":   {"FCB_DATABASE_DRIVER", "DATABASE_TYPE", "DB_TYPE"},
	"database.db_name":  {"FCB_DATABASE_DB_NAME", "DATABASE_NAME", "DB_NAME"},
	"database.host":     {"FCB_DATABASE_HOST", "DATABASE_HOST", "DB_HOST"},
	"database.port":     {"FCB_DATABASE_PORT", "DATABASE_PORT", "DB_PORT"},
	"database.user":     {"FCB_DATABASE_USER", "DATABASE_USER", "DB_USER"},
	"database.password": {"FCB_DATABASE_PASSWORD", "DATABASE_PASS", "DB_PASS"},
	// redis
	"redis.host":     {"FCB_REDIS_HOST", "REDIS_HOST"},
	"redis.port":     {"FCB_REDIS_PORT", "REDIS_PORT"},
	"redis.password": {"FCB_REDIS_PASSWORD", "REDIS_PASSWORD"},
	"redis.db":       {"FCB_REDIS_DB", "REDIS_DB"},
	// app
	"app.datapath":   {"FCB_DATA_PATH", "DATA_PATH"},
	"app.production": {"FCB_PRODUCTION", "PRODUCTION"},
	// user
	"user.jwt_secret":              {"FCB_JWT_SECRET", "JWT_SECRET"},
	"user.allow_user_registration": {"FCB_USER_ALLOW_REGISTRATION"},
	// upload
	"upload.open_upload": {"FCB_OPEN_UPLOAD", "OPEN_UPLOAD"},
	"upload.upload_size": {"FCB_UPLOAD_SIZE", "UPLOAD_SIZE"},
	// storage
	"storage.type":         {"FCB_STORAGE_TYPE"},
	"storage.storage_path": {"FCB_STORAGE_PATH"},
	// download
	"download.s3_direct_download": {"FCB_DOWNLOAD_S3_DIRECT"},
	// observability
	"observability.metrics.enabled": {"FCB_METRICS_ENABLED"},
	"observability.metrics.path":    {"FCB_METRICS_PATH"},
	"observability.tracing.enabled": {"FCB_TRACING_ENABLED"},
	// security
	"security.cors.allow_origins":          {"FCB_CORS_ALLOW_ORIGINS"},
	"security.cors.enable_hsts":            {"FCB_ENABLE_HSTS"},
	"security.trusted_proxies":             {"FCB_TRUSTED_PROXIES"},
	"security.download_token.enabled":      {"FCB_DOWNLOAD_TOKEN_ENABLED"},
	"security.lockout.enabled":             {"FCB_LOCKOUT_ENABLED"},
	"security.lockout.max_attempts":        {"FCB_LOCKOUT_MAX_ATTEMPTS"},
	"security.ssrf.allow_private_networks": {"FCB_SSRF_ALLOW_PRIVATE"},
	// notify
	"notify.webhook_url": {"FCB_WEBHOOK_URL", "WEBHOOK_URL"},
	// mcp
	"mcp.enabled": {"FCB_MCP_ENABLED"},
	// upload 安全项
	"upload.text_max_bytes":     {"FCB_TEXT_MAX_BYTES"},
	"upload.allowed_extensions": {"FCB_UPLOAD_ALLOWED_EXTENSIONS"},
	"upload.enable_magic_check": {"FCB_ENABLE_MAGIC_CHECK"},
	// rate_limit
	"rate_limit.enabled":       {"FCB_RATE_LIMIT_ENABLED"},
	"rate_limit.global_qps":    {"FCB_RATE_LIMIT_GLOBAL_QPS"},
	"rate_limit.upload_qps":    {"FCB_RATE_LIMIT_UPLOAD_QPS"},
	"rate_limit.download_qps":  {"FCB_RATE_LIMIT_DOWNLOAD_QPS"},
	"rate_limit.login_qps":     {"FCB_RATE_LIMIT_LOGIN_QPS"},
	"rate_limit.block_seconds": {"FCB_RATE_LIMIT_BLOCK_SECONDS"},
	"rate_limit.use_redis":     {"FCB_RATE_LIMIT_USE_REDIS"},
}

// bindEnvironment 把环境变量绑定到 viper 配置 key。
// 列表中靠前的 env 名优先（viper BindEnv 只绑定第一个非空）。
func bindEnvironment(v *viper.Viper) {
	for key, envs := range envBindings {
		// 绑定所有候选 env 名；viper 会取最后一个 BindEnv 的值，
		// 因此我们逐个检查并显式设置，确保优先级正确。
		for _, env := range envs {
			if val, ok := os.LookupEnv(env); ok {
				v.Set(key, val)
				break
			}
		}
	}
}

// insecureDefaultSecrets 已知的不安全默认/占位密钥（全环境禁止使用）。
var insecureDefaultSecrets = map[string]string{
	"FileCodeBox2025JWT":                    "user.jwt_secret",
	"filecodebox-dev-signing-key-change-me": "presign signing key",
	"FileCodeBox2025SecretKey":              "auth default secret",
	"please-change-me":                      "placeholder secret",
	"dev-only-change-me":                    "dev placeholder secret",
	"dev-only-change-me-to-random-32chars":  "dev placeholder secret",
}

// validateSecrets 全环境校验敏感配置，避免使用默认/弱密钥启动（fail-fast）。
// 所有环境（含开发）都必须设置强随机的 user.jwt_secret。
func validateSecrets(cfg *Config) error {
	// jwt_secret：空或命中黑名单一律拒绝
	if sec := cfg.User.JWTSecret; sec == "" || insecureDefaultSecrets[sec] != "" {
		return fmt.Errorf("a secure user.jwt_secret is required in ALL environments: current value is empty or a known default; set FCB_JWT_SECRET env to a strong random string (>=32 chars)")
	}
	return nil
}

// InitDatabase 初始化数据库。
//
// 迁移策略（按配置）：
//   - database.migrate=true：先执行版本化迁移(migrations/*.sql)，适合生产/需要版本控制
//   - database.auto_migrate（默认 true）：GORM AutoMigrate，开发友好、自动补表/列
//   - 两者可共存：版本化迁移建表后，AutoMigrate 兜底补充新字段
func InitDatabase(config *conf.DatabaseConfig) (*gorm.DB, error) {
	// 创建数据目录
	if config.Driver == "sqlite" {
		dbPath := config.DBName
		if dbPath != ":memory:" {
			log.Printf("SQLite database path: %s", dbPath)
		}
	}

	// 初始化数据库连接（db.Init 内部会执行 AutoMigrate 兜底）
	err := db.Init(config)
	if err != nil {
		return nil, fmt.Errorf("failed to connect database: %w", err)
	}

	database := db.GetDB()

	// 版本化迁移（企业级，可选）
	if config.Migrate {
		log.Println("Running versioned database migrations...")
		migrator, err := db.NewMigrator(database, config.Driver)
		if err != nil {
			return nil, fmt.Errorf("failed to create migrator: %w", err)
		}
		applied, err := migrator.Up()
		if err != nil {
			return nil, fmt.Errorf("versioned migration failed: %w", err)
		}
		if len(applied) > 0 {
			log.Printf("Applied %d migration(s): %v", len(applied), applied)
		} else {
			log.Println("No new migrations to apply (already up to date)")
		}
		// AutoMigrate 兜底：补充 baseline 未覆盖的表（如 file_previews/notifies）
		if err := database.AutoMigrate(
			&model.FilePreview{},
		); err != nil {
			logger.Error("AutoMigrate fallback for previews failed", zap.Error(err))
		}
	}

	log.Println("Database initialized successfully")
	return database, nil
}

// CreateDefaultAdmin 创建默认管理员。
//
// 密码来源（优先级）：FCB_ADMIN_PASSWORD 环境变量 > 默认 admin123。
// 密码用 bcrypt 现场哈希（此前硬编码的哈希与 admin123 不匹配，导致管理员无法登录）。
// 生产环境务必通过 FCB_ADMIN_PASSWORD 注入强密码并在首次登录后修改。
func CreateDefaultAdmin(database *gorm.DB) error {
	var count int64
	database.Model(&model.User{}).Where("role = ?", "admin").Count(&count)

	if count > 0 {
		log.Println("Admin user already exists")
		return nil
	}

	// 密码：env 注入优先，否则默认 admin123
	password := os.Getenv("FCB_ADMIN_PASSWORD")
	if password == "" {
		password = "admin123"
	}

	// 现场生成 bcrypt 哈希（避免硬编码哈希与明文不一致）
	hashed, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return fmt.Errorf("failed to hash admin password: %w", err)
	}

	admin := &model.User{
		Username:     "admin",
		Email:        "admin@filecodebox.local",
		PasswordHash: string(hashed),
		Nickname:     "Administrator",
		Role:         "admin",
		Status:       "active",
	}

	if err := database.Create(admin).Error; err != nil {
		return fmt.Errorf("failed to create admin user: %w", err)
	}

	if os.Getenv("FCB_ADMIN_PASSWORD") == "" {
		logger.Warn("Default admin created with default password 'admin123' — change it immediately in production (set FCB_ADMIN_PASSWORD for a custom one)")
	} else {
		logger.Info("Default admin created with password from FCB_ADMIN_PASSWORD")
	}
	return nil
}

var (
	database *gorm.DB
	config   *Config
)

// Bootstrap 应用程序启动入口(兼容入口,等价于无选项的 BootstrapWithOptions)。
// configPath 为空时依次回退到 CONFIG_PATH 环境变量、默认 configs/config.yaml。
func Bootstrap(configPath string) (*server.Hertz, error) {
	return BootstrapWithOptions(configPath)
}

// BootstrapWithOptions 带函数选项的启动入口。
// 可用选项见 options.go(如 WithStaticDir 覆盖前端静态资源目录)。
func BootstrapWithOptions(configPath string, opts ...Option) (*server.Hertz, error) {
	staticOpts = applyOptions(opts...)

	// 1. 初始化配置
	var err error
	config, err = InitConfig(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to init config: %w", err)
	}

	// 设置全局配置（供其他包访问）
	conf.SetGlobalConfig(config)

	// 1.1 注入 JWT secret 到 auth 包（覆盖硬编码默认值）。
	// 此前 jwt.go 使用硬编码 "FileCodeBox2025SecretKey"，且与 config 的 jwt_secret
	// 不一致，导致配置中的 secret 从未生效。此处统一从配置/env 读取。
	auth.SetJWTSecret(config.User.JWTSecret)

	// 2. 初始化日志
	loggerConfig := &logger.Config{
		Level:      config.Log.Level,
		Filename:   config.Log.Filename,
		MaxSize:    config.Log.MaxSize,
		MaxBackups: config.Log.MaxBackups,
		MaxAge:     config.Log.MaxAge,
		Compress:   config.Log.Compress,
	}
	if err := logger.Init(loggerConfig); err != nil {
		return nil, fmt.Errorf("failed to init logger: %w", err)
	}
	logger.Info("JWT secret loaded from configuration",
		zap.Bool("production", config.IsProduction()))

	// 3. 初始化数据库
	database, err = InitDatabase(&config.Database)
	if err != nil {
		return nil, fmt.Errorf("failed to init database: %w", err)
	}

	// 3.5 初始化 Redis（匿名取件码 / presign 会话 / 分布式限流依赖）。
	// 此前 bootstrap 从不调用 redis.Init，GetClient() 恒为 nil，
	// 导致匿名取件与预签名直传在运行期必然失败（P0）。
	// Redis 连不上时降级启动并告警：server 主体功能仍可用，
	// 匿名取件接口会返回"Redis 未配置"的明确错误。
	if config.Redis.Host != "" {
		if err := redis.Init(&config.Redis); err != nil {
			logger.Error("Redis init failed — anonymous pickup & presign degraded",
				zap.String("addr", config.Redis.Addr()), zap.Error(err))
		} else {
			logger.Info("Redis initialized", zap.String("addr", config.Redis.Addr()))
		}
	} else {
		logger.Warn("Redis not configured (redis.host empty) — anonymous pickup & presign disabled")
	}

	// 3.6 安全中间件配置注入：
	//   - 可信代理 CIDR（决定 XFF 是否被采信，防伪造绕过限流）
	//   - 下载令牌签名密钥（复用 jwt_secret 派生，独立注入便于轮换）
	if err := middleware.SetTrustedProxies(config.Security.TrustedProxies); err != nil {
		return nil, fmt.Errorf("invalid security.trusted_proxies: %w", err)
	}
	securityPkg.SetDownloadTokenSecret("fcb-dl:" + config.User.JWTSecret)

	// 4. 创建默认管理员
	if err := CreateDefaultAdmin(database); err != nil {
		logger.Error("Failed to create default admin", zap.Error(err))
	}

	// 4.5 初始化预览服务
	if err := initPreviewService(); err != nil {
		logger.Error("Failed to init preview service", zap.Error(err))
	}

	// 4.6 初始化新服务（thrift IDL 对应：notify/presign/anonymous/ratelimit）
	initThriftIDLServices(database)

	// 5. 创建 HTTP 服务器
	port := config.Server.Port
	if port == 0 {
		port = 12345
	}
	// 上传 body 上限（应用层强制，覆盖 Hertz 默认无限制）
	uploadSize := int(config.Upload.UploadSize)
	if uploadSize <= 0 {
		uploadSize = 10 * 1024 * 1024 // 默认 10MB
	}
	h := server.New(
		server.WithHostPorts(fmt.Sprintf("%s:%d", config.Server.Host, port)),
		server.WithMaxRequestBodySize(uploadSize),
	)

	// 可观测性：初始化 Prometheus 指标（在注册中间件前完成）
	if config.Observability.Metrics.Enabled {
		middleware.NewMetrics()
		logger.Info("Prometheus metrics enabled",
			zap.String("path", config.Observability.Metrics.Path))
	}

	// 安全：配置安全响应头（HSTS 仅在显式启用时开启，避免非 HTTPS 部署锁死）
	middleware.SetSecurityHeadersConfig(middleware.SecurityHeadersConfig{
		EnableHSTS: config.Security.CORS.EnableHSTS,
	})
	if config.Security.CORS.EnableHSTS {
		logger.Info("HSTS enabled (ensure HTTPS deployment)")
	}

	// 全局中间件链（顺序敏感）：
	//   Recovery → RequestID → AccessLog → Metrics → SecurityHeaders → CORS → handler
	//   - Recovery 最外层，捕获任意 panic 转 500，避免进程崩溃
	//   - RequestID 生成/透传 trace_id（X-Trace-Id），写入 ctx 供下游使用
	//   - AccessLog 结构化访问日志（带 trace_id），在 handler 执行后记录 status
	//   - Metrics 采集 RED 指标（延迟/计数/在途），在 handler 执行后记录
	//   - SecurityHeaders 设置 X-Content-Type-Options / X-Frame-Options / HSTS 等
	//   - CORS 跨域，最贴近 handler
	h.Use(middleware.Recovery())
	h.Use(middleware.RequestID())
	h.Use(middleware.AccessLog())
	if config.Observability.Metrics.Enabled {
		h.Use(middleware.MetricsMiddleware())
	}
	h.Use(middleware.SecurityHeaders())
	h.Use(CORS())

	// 限流：路径感知，按接口类型选择限流维度（登录/上传/下载）。
	// 防止暴力破解登录、取件码枚举、上传下载 DoS。
	// 配置来自 rate_limit 段（默认值见 middleware.DefaultRateLimitConfig），
	// use_redis=true 且 Redis 可用时多实例共享计数。
	rl := middleware.InitDefaultRateLimiter(middleware.RateLimitConfigFromConf())
	rl.SetRedis(redis.GetClient())
	h.Use(func(ctx context.Context, c *app.RequestContext) {
		path := string(c.Request.URI().Path())
		switch {
		case strings.HasPrefix(path, "/admin/login"),
			strings.HasPrefix(path, "/api/v1/user/login"):
			rl.LoginMiddleware()(ctx, c)
		case strings.HasPrefix(path, "/anonymous/generate"),
			strings.HasPrefix(path, "/anonymous/retrieve"),
			strings.HasPrefix(path, "/api/v1/presign"),
			strings.HasPrefix(path, "/api/v1/chunk"):
			rl.UploadMiddleware()(ctx, c)
		case strings.Contains(path, "/download"):
			rl.DownloadMiddleware()(ctx, c)
		default:
			c.Next(ctx)
		}
	})

	// 6. 注册路由
	router.GeneratedRegister(h)

	// 7. 注册自定义路由
	customizedRegister(h)

	logger.Info("Application bootstrap completed successfully")
	return h, nil
}

// Cleanup 清理资源
func Cleanup() {
	logger.Info("Cleaning up resources...")

	if database != nil {
		if err := db.Close(); err != nil {
			logger.Error("Failed to close database", zap.Error(err))
		}
	}

	logger.Sync()
}

// customizedRegister 注册自定义路由（不走 thrift IDL 生成）。
//
// 历史背景：该函数此前为空，导致前端 SPA 静态服务、Swagger 文档、
// “我的分享管理”与“用户通知”REST API 全部未生效。本函数把此前残留在
// 根目录 router.go（死代码）中的逻辑正式接线，使单二进制 / Docker 部署
// 即可打开前端页面并使用全部功能。
func customizedRegister(r *server.Hertz) {
	// ===== OpenAPI 文档（Swagger UI）=====
	r.GET("/openapi.json", customHandler.OpenAPISpec)

	// ===== presign 预签名直传端点（gen router 未注册，在此补）=====
	r.PUT("/api/v1/presign/upload-direct/:uploadID", presignHandler.UploadDirect)

	// ===== token 刷新端点（前端 401 拦截器调用，换发新 token）=====
	r.POST("/api/v1/user/refresh", func(ctx context.Context, c *app.RequestContext) {
		authHeader := string(c.GetHeader("Authorization"))
		if authHeader == "" || !strings.HasPrefix(authHeader, "Bearer ") {
			c.JSON(consts.StatusUnauthorized, map[string]interface{}{"code": 401, "message": "missing token"})
			return
		}
		oldToken := strings.TrimPrefix(authHeader, "Bearer ")
		newToken, err := auth.RefreshToken(oldToken)
		if err != nil {
			c.JSON(consts.StatusUnauthorized, map[string]interface{}{"code": 401, "message": "token invalid or expired"})
			return
		}
		c.JSON(consts.StatusOK, map[string]interface{}{
			"code": 200, "message": "ok",
			"data": map[string]string{"token": newToken},
		})
	})

	// ===== 公开配置端点（前端 configStore 启动时拉取）=====
	// 前端 publicApi.getConfig() 请求 /api/config 获取站点配置（名称、上传限制等），
	// 此前端点缺失导致前端启动报 "获取配置失败: Network Error"。此处补齐。
	r.GET("/api/config", publicConfigHandler)

	// ===== robots.txt（对标上游 SEO 可配；输出 ui.robots_text）=====
	r.GET("/robots.txt", func(ctx context.Context, c *app.RequestContext) {
		content := config.UI.RobotsText
		if content == "" {
			content = "User-agent: *\nDisallow: /\n"
		}
		c.Header("Content-Type", "text/plain; charset=utf-8")
		c.String(consts.StatusOK, content)
	})

	// ===== MCP server（Model Context Protocol；AI 客户端集成，上游没有的差异化能力）=====
	// Streamable HTTP 传输：POST /api/v1/mcp（JSON-RPC 2.0），管理员 JWT 认证。
	// Claude Desktop 等标准客户端以 Authorization: Bearer <admin token> 接入。
	if config.MCP.Enabled {
		// initThriftIDLServices 未跑（如轻量测试环境）时惰性兜底：
		// share service 缺席时 share_text 工具会明确报错，协议处理不受影响
		if mcpService == nil {
			mcpService = mcpApp.NewService(config.App.Version)
		}
		r.POST("/api/v1/mcp", middleware.AdminMiddleware(), func(ctx context.Context, c *app.RequestContext) {
			body := c.Request.Body()
			status, respBody := mcpService.Handle(ctx, body)
			if status == 202 {
				c.SetStatusCode(consts.StatusAccepted)
				return
			}
			c.Header("Content-Type", "application/json")
			c.SetStatusCode(status)
			_, _ = c.Write(respBody)
		})
	}

	// ===== logout 端点（补齐基准 P1 缺口：显式注销 + token 黑名单）=====
	r.POST("/api/v1/user/logout", func(ctx context.Context, c *app.RequestContext) {
		authHeader := string(c.GetHeader("Authorization"))
		if !strings.HasPrefix(authHeader, "Bearer ") {
			c.JSON(consts.StatusUnauthorized, map[string]interface{}{"code": 401, "message": "missing token"})
			return
		}
		token := strings.TrimPrefix(authHeader, "Bearer ")
		// 黑名单 TTL = token 剩余有效期（过期后自然失效，无需清理任务）
		if claims, err := auth.ParseToken(token); err == nil {
			remaining := time.Until(claims.ExpiresAt.Time)
			auth.RevokeToken(ctx, token, remaining)
		}
		c.JSON(consts.StatusOK, map[string]interface{}{"code": 200, "message": "已退出登录"})
	})

	// ===== check-auth 端点（前端启动时校验 token 有效性并取回用户信息）=====
	r.GET("/api/v1/user/check-auth", customMw.UserAuth(), func(ctx context.Context, c *app.RequestContext) {
		uid, _ := c.Get("user_id")
		username, _ := c.Get("username")
		role, _ := c.Get("role")
		c.JSON(consts.StatusOK, map[string]interface{}{
			"code":    200,
			"message": "ok",
			"data": map[string]interface{}{
				"id":       uid,
				"username": username,
				"role":     role,
			},
		})
	})

	// ===== 管理端操作审计日志查询（P0-C：此前审计只写不查/没写）=====
	// + 管理端增强：用户 CRUD / 文件管理 / 富统计（AdminMiddleware 保护）
	adminAPI := r.Group("/admin", middleware.AdminMiddleware())
	{
		adminAPI.GET("/activities", func(ctx context.Context, c *app.RequestContext) {
			page, pageSize := 1, 20
			if v, err := strconv.Atoi(c.Query("page")); err == nil && v > 0 {
				page = v
			}
			if v, err := strconv.Atoi(c.Query("page_size")); err == nil && v > 0 && v <= 200 {
				pageSize = v
			}
			query := model.AdminOperationLogQuery{
				Action:   c.Query("action"),
				Actor:    c.Query("actor"),
				Page:     page,
				PageSize: pageSize,
			}
			if s := c.Query("success"); s == "true" || s == "false" {
				b := s == "true"
				query.Success = &b
			}
			repo := dao.NewAdminOperationLogRepository()
			logs, total, err := repo.List(ctx, query)
			if err != nil {
				resp.NewErrorWithMessage(c, 50001, "查询审计日志失败: "+err.Error())
				return
			}
			resp.Page(c, logs, total, page, pageSize)
		})

		// 用户管理 CRUD（此前只有 list + status 切换）
		adminAPI.POST("/users", customHandler.AdminCreateUser)
		adminAPI.PUT("/users/:id", customHandler.AdminUpdateUser)
		adminAPI.DELETE("/users/:id", customHandler.AdminDeleteUser)
		adminAPI.POST("/users/:id/reset-password", customHandler.AdminResetUserPassword)
		adminAPI.GET("/users/filter", customHandler.AdminListUsersFiltered)

		// 文件管理（此前只有 list + 单删）
		adminAPI.GET("/files/:id/download", customHandler.AdminDownloadFile)
		adminAPI.GET("/files/:id", customHandler.AdminFileDetail)
		adminAPI.PUT("/files/:id", customHandler.AdminUpdateFile)
		adminAPI.POST("/files/batch-delete", customHandler.AdminBatchDeleteFiles)
		adminAPI.POST("/files/batch-extend", customHandler.AdminBatchExtendFiles)

		// Dashboard 富指标
		adminAPI.GET("/stats/enhanced", customHandler.AdminEnhancedStats)
		adminAPI.GET("/stats/trend", customHandler.AdminStatsTrend)

		// 传输日志（此前前端调用的端点不存在、表无写入方，页面一直空数据）
		adminAPI.GET("/logs/transfer", customHandler.AdminTransferLogs)
	}

	// ===== 前端构建产物静态资源 =====
	// Vite 输出的 index.html 用根级绝对路径引用资源（/assets/xxx、/vite.svg），
	// 故把 ./static/assets 挂到 /assets。用 StaticFS 正确处理 Range 请求、MIME、
	// 缓存（NoRoute 里手写的 c.File 对大文件 ES module 的 Range/缓冲处理不够稳定，
	// 会导致浏览器 "Failed to fetch dynamically imported module"）。
	r.StaticFS("/assets", &app.FS{
		Root:          filepath.Join(staticOpts.StaticDir, "assets"),
		PathRewrite:   app.NewPathSlashesStripper(1),
		CacheDuration: 7 * 24 * time.Hour,
	})

	// ===== Prometheus 指标端点（独立内网 server，默认不暴露到主端口）=====
	// 开启时绑定 127.0.0.1:9090（可用 FCB_METRICS_ADDR 配置），供同节点 Prometheus 抓取。
	// 主 server 不注册 /metrics，避免公网泄露内部指标。
	if config.Observability.Metrics.Enabled {
		metricsPath := config.Observability.Metrics.Path
		if metricsPath == "" {
			metricsPath = "/metrics"
		}
		metricsAddr := os.Getenv("FCB_METRICS_ADDR")
		if metricsAddr == "" {
			metricsAddr = "127.0.0.1:9090"
		}
		metricsServer := server.New(server.WithHostPorts(metricsAddr))
		metricsServer.GET(metricsPath, metricsHandler)
		go func() {
			logger.Info("metrics server listening", zap.String("addr", metricsAddr))
			if err := metricsServer.Run(); err != nil {
				logger.Error("metrics server failed", zap.Error(err))
			}
		}()
	}

	// ===== 深度就绪检查 =====
	// /readyz 检查 DB 等依赖连通性，供 K8s readinessProbe 使用；
	// 依赖不可用时返回 503，避免流量打到未就绪实例。
	// （IDL 生成的 /ready 为轻量 stub，此处用 /readyz 做深度检查以避免路由冲突）
	r.GET("/readyz", readinessHandler)

	// ===== 自定义 REST API（需用户 JWT 认证）=====
	apiV1 := r.Group("/api/v1", customMw.UserAuth())
	{
		// 我的分享管理（批量删除 / 批量延期 / 恢复 / 永久删除）
		userShares := apiV1.Group("/user/shares")
		userShares.GET("", customHandler.ListUserShares)
		userShares.POST("/batch-delete", customHandler.BatchDeleteUserShares)
		userShares.POST("/batch-extend", customHandler.BatchExtendUserShares)
		userShares.POST("/:code/restore", customHandler.RestoreUserShare)
		userShares.DELETE("/:code/hard", customHandler.HardDeleteUserShare)

		// 用户站内通知（列表 / 未读数 / 标记已读）
		apiV1.GET("/notifies/mine", customHandler.ListMyNotifications)
		apiV1.GET("/notifies/unread-count", customHandler.UnreadNotifyCount)
		apiV1.POST("/notifies/mark-read", customHandler.MarkNotifyRead)
	}

	// ===== 前端 SPA 静态资源服务 =====
	// Vite 构建的 index.html 使用根级绝对路径引用资源（/assets/xxx.js、/vite.svg），
	// 因此不能把资源挂在 /static 前缀下。这里采用"文件优先 + SPA 回退"策略：
	//   1. 静态资源请求（/assets/*、/vite.svg 等带扩展名路径）→ 从 ./static 读取
	//   2. 非 API 的其他路径 → 回退 index.html（交给前端 hash 路由）
	//   3. API 路径未命中 → 404 JSON
	r.NoRoute(func(ctx context.Context, c *app.RequestContext) {
		path := string(c.Request.URI().Path())
		if isAPIPath(path) {
			c.JSON(consts.StatusNotFound, map[string]interface{}{
				"code":    404,
				"message": "API endpoint not found",
			})
			return
		}
		// 静态资源：尝试从 ./static 下读取（去掉前导 /）
		if tryServeStatic(c, path) {
			return
		}
		// 其余路径回退到 SPA index.html
		c.File(filepath.Join(staticOpts.StaticDir, "index.html"))
	})
}

// apiPathPrefixes 仅含"纯 API"前缀——这些前缀下不存在前端页面，
// 未命中路由时返回 JSON 404，而不是回退到 SPA index.html。
//
// 注意：/user、/admin、/anonymous、/share 同时是后端 API 前缀与前端 hash
// 路由的页面路径（如 /user/login、/admin/dashboard、/share/:code）。前端使用
// createWebHashHistory，浏览器实际只请求 "/"，但若用户直接访问这些 history
// 路径（如刷新、外链），应回退到 SPA 由前端路由处理，而非返回 API 404。
// 因此它们不在本列表中。
var apiPathPrefixes = []string{
	"/api", "/chunk", "/notifies",
	"/health", "/live", "/ready", "/readyz", "/ping", "/version",
	"/openapi", "/metrics", "/preview", "/qrcode", "/setup",
}

// isAPIPath 判断路径是否属于后端 API（而非前端 SPA 路由）。
func isAPIPath(path string) bool {
	for _, p := range apiPathPrefixes {
		if path == p || strings.HasPrefix(path, p+"/") {
			return true
		}
	}
	return false
}

// staticFileExtensions 视为静态资源（而非 SPA 路由）的文件扩展名。
// Vite 构建产物（js/css/图片/字体等）走这里直接返回文件。
var staticFileExtensions = map[string]bool{
	".js": true, ".mjs": true, ".css": true,
	".html": true, ".svg": true, ".png": true, ".jpg": true, ".jpeg": true,
	".gif": true, ".ico": true, ".webp": true, ".woff": true, ".woff2": true,
	".ttf": true, ".eot": true, ".map": true, ".json": true, ".txt": true,
}

// tryServeStatic 尝试从 ./static 目录服务静态资源。
// 命中（文件存在且是静态资源扩展名）时写入响应并返回 true，否则返回 false。
// 用于在 NoRoute 中优先服务 Vite 构建产物（/assets/xxx.js 等），再回退 SPA。
func tryServeStatic(c *app.RequestContext, path string) bool {
	// 仅对带静态资源扩展名的路径尝试（避免目录穿越和无谓的文件系统查找）
	ext := strings.ToLower(filepath.Ext(path))
	if !staticFileExtensions[ext] {
		return false
	}
	// 去掉前导 /，拼接到 static 根目录；filepath.Join 会清理 ../ 等穿越
	rel := strings.TrimPrefix(path, "/")
	fullPath := filepath.Join(staticOpts.StaticDir, rel)
	info, err := os.Stat(fullPath)
	if err != nil || info.IsDir() {
		return false
	}
	c.File(fullPath)
	return true
}

// publicConfigHandler 返回公开配置（前端 configStore 启动时拉取）。
// 返回结构与前端 PublicConfig 接口对齐：
// name / description / uploadSize / enableChunk / openUpload / expireStyle / initialized。
// initialized 为真实初始化状态（按管理员用户数判断）；查询出错时保守返回 true，
// 避免数据库抖动把正常实例的前端误导入 Setup 流程。
func publicConfigHandler(ctx context.Context, c *app.RequestContext) {
	initialized := true
	if ok, err := publicSetupSvc.IsSystemInitialized(ctx); err == nil {
		initialized = ok
	}
	resp.Success(c, map[string]interface{}{
		"name":        config.App.Name,
		"description": config.App.Description,
		"uploadSize":  config.Upload.UploadSize,
		"enableChunk": config.Upload.EnableChunk,
		"openUpload":  config.Upload.OpenUpload,
		// 前端 expireStyle 下拉选项（与 utils.CalculateExpireTime 支持的风格对齐）
		"expireStyle": []string{"minute", "hour", "day", "week", "month", "year", "forever"},
		"initialized": initialized,
	})
}

// publicSetupSvc /api/config 查询初始化状态用（无依赖，惰性安全）
var publicSetupSvc = setupApp.NewService()

// metricsHandler 暴露 Prometheus 指标（/metrics）。
// Hertz 与标准 net/http 接口不同，不能直接用 promhttp.Handler()，
// 这里手动 gather 指标并用 expfmt 文本格式输出到 buffer 再写入响应。
func metricsHandler(ctx context.Context, c *app.RequestContext) {
	mfs, err := prometheus.DefaultGatherer.Gather()
	if err != nil {
		c.JSON(consts.StatusInternalServerError, map[string]interface{}{
			"code":    500,
			"message": "failed to gather metrics: " + err.Error(),
		})
		return
	}
	var buf bytes.Buffer
	enc := expfmt.NewEncoder(&buf, expfmt.NewFormat(expfmt.TypeTextPlain))
	for _, mf := range mfs {
		if err := enc.Encode(mf); err != nil {
			logger.Error("failed to encode metric", zap.String("name", mf.GetName()), zap.Error(err))
		}
	}
	c.SetStatusCode(consts.StatusOK)
	c.Response.Header.SetContentType(string(expfmt.NewFormat(expfmt.TypeTextPlain)))
	_, _ = c.Write(buf.Bytes())
}

// readinessHandler 深度就绪检查：校验 DB 连通性，失败返回 503。
// 供 K8s readinessProbe 使用——依赖未就绪时不接流量。
func readinessHandler(ctx context.Context, c *app.RequestContext) {
	checks := map[string]bool{}
	allOK := true

	// DB ping
	if database != nil {
		if sqlDB, err := database.DB(); err == nil {
			if err := sqlDB.Ping(); err == nil {
				checks["database"] = true
			} else {
				checks["database"] = false
				allOK = false
			}
		} else {
			checks["database"] = false
			allOK = false
		}
	} else {
		checks["database"] = false
		allOK = false
	}

	status := "ready"
	httpStatus := consts.StatusOK
	if !allOK {
		status = "not ready"
		httpStatus = consts.StatusServiceUnavailable
	}
	c.JSON(httpStatus, map[string]interface{}{
		"status": status,
		"checks": checks,
	})
}

func initPreviewService() error {
	previewConfig := &previewPkg.Config{
		EnablePreview:    true,
		ThumbnailWidth:   300,
		ThumbnailHeight:  200,
		MaxFileSize:      50 * 1024 * 1024,
		PreviewCachePath: "./data/previews",
		FFmpegPath:       "ffmpeg",
	}

	return previewPkg.InitService(previewConfig)
}

// initThriftIDLServices 初始化 thrift IDL 对应的新服务
// 关联 internal/app/ → gen/http/handler/ 各 SetXxx 入口
func initThriftIDLServices(database *gorm.DB) {
	// 0. 恢复 DB 持久化的运行时存储配置（管理端在线切换的后端类型/s3/webdav）
	restoreRuntimeStorage()

	// 1. notify service（走 DAO，内部用全局 db.GetDB()）
	notifyApp := notifyAppService.NewService()
	notifyHandler.SetDB(database)
	// 1.1 注入定制路由的 notify service
	customHandler.SetNotifyService(notifyApp)

	// 2. presign service（需要 Redis + baseURL + signingKey + share service）
	// baseURL 优先用配置的对外地址（server.base_url），否则用 host:port
	baseURL := config.Server.BaseURL
	if baseURL == "" {
		baseURL = fmt.Sprintf("http://%s:%d", config.Server.Host, config.Server.Port)
	}
	// presign 签名密钥：优先专用 FCB_PRESIGN_SIGNING_KEY，否则复用 jwt_secret
	signingKey := os.Getenv("FCB_PRESIGN_SIGNING_KEY")
	if signingKey == "" {
		signingKey = config.User.JWTSecret
	}
	presignHandler.SetService(redis.GetClient(),
		baseURL,
		signingKey)
	// 2.1 注入 share service（Complete 时写分享表）
	shareSvc := shareService.NewService(baseURL, getBootstrapStorageService())
	presignHandler.SetShareService(shareSvc)
	// 2.1.1 注入存储服务（presign 直传按当前激活后端落盘）
	presignHandler.SetStorage(getBootstrapStorageService())
	// 2.1.2 注入真预签名直传能力（s3 后端时 Init 签发对象存储直传 URL）
	presignHandler.SetObjectStore(getBootstrapStorageService())
	// 2.2 注入定制路由的 share service
	customHandler.SetShareService(shareSvc)
	// 2.3 注入 notify service（取件时给 owner 发通知）
	shareSvc.SetNotifyService(notifyApp) // *Service 已实现 CreateForUserSimple
	// 2.3.1 外部 Webhook 推送渠道（notify.created 事件；空 = 禁用）
	notifyApp.SetWebhookURL(config.Notify.WebhookURL)
	// 2.4 注入 user service：上传统计（此前从未接线，用户统计恒为 0）
	// + 存储配额强制检查（user_quota / 用户级 max_storage_quota）
	userSvc := userService.NewService()
	shareSvc.SetUserService(userSvc)
	shareSvc.SetQuotaChecker(userSvc)

	// 3. anonymous service（需要 Redis）
	anonHandler.SetService(redis.GetClient())

	// 4. ratelimit service（直接用 default limiter）
	ratelimitHandler.SetLimiter(middleware.GetDefaultRateLimiter())

	// 4.5 失败锁定器 + JWT 注销黑名单（Redis 可用时共享，否则内存兜底）
	middleware.InitDefaultLockout(redis.GetClient())
	auth.SetBlacklistRedis(redis.GetClient())

	// 4.6 管理端 admin service（单一实例：路由增强与存储配置持久化共用，
	// 避免 runtime_storage 段在多实例间读写漂移——字段本身可跨实例 JSON 往返）
	adminSvc := adminApp.NewService()

	// 4.6.1 storage 管理 service（连接测试/在线切换：认证级 Probe + 热重载 + 持久化）
	storageSvc := storageApp.NewService()
	storageSvc.SetRuntime(getBootstrapStorageService())
	storageSvc.SetPersister(adminSvc)
	storageHandler.SetService(storageSvc)

	// 4.7 管理端增强服务注入（用户 CRUD/文件管理/富统计）
	customHandler.SetManageServices(adminSvc, userSvc, getBootstrapStorageService())

	// 5. 自动迁移 notify 表 + file_codes viewer 字段
	if err := database.AutoMigrate(&model.Notify{}); err != nil {
		logger.Error("Failed to migrate notify table", zap.Error(err))
	} else {
		logger.Info("Notify table migrated")
	}
	if err := database.AutoMigrate(&model.FileCode{}); err != nil {
		logger.Error("Failed to migrate file_codes table", zap.Error(err))
	} else {
		logger.Info("FileCode table migrated (viewer fields added)")
	}

	// 6. 注入 storage 到 admin handler 的 service（过期清理删物理文件）
	bootstrapStorage := getBootstrapStorageService()
	adminHandler.SetStorage(bootstrapStorage)

	// 6.5 统一存储实例注入 chunk/share handler（消除懒加载单例路径基分歧：
	// 分片合并写入 data/uploads/<rel>，下载却找 data/uploads/uploads/<rel>）
	chunkHandler.SetStorage(bootstrapStorage)
	shareHandler.SetStorage(bootstrapStorage)

	// 6.5 MCP server（AI 客户端集成）：统计/维护走带 storage 的 admin service，
	//     分享创建走 share service（复用配额/审计链路）
	if config.MCP.Enabled {
		mcpService = mcpApp.NewService(config.App.Version)
		mcpService.SetAdminService(cleanupSvcWithStorage(bootstrapStorage))
		mcpService.SetShareService(shareSvc)
	}

	// 7. 启动过期文件定时清理（默认每小时，删 DB 记录 + 物理文件）
	//    独立 admin service 实例（避免与 handler 实例竞争），注入 storage
	cleanupSvc := adminApp.NewService()
	cleanupSvc.SetStorage(bootstrapStorage)
	go startExpiredFileCleanup(cleanupSvc)
}

// mcpService MCP server 实例（initThriftIDLServices 装配，customizedRegister 挂路由）
var mcpService *mcpApp.Service

// cleanupSvcWithStorage 创建带 storage 的 admin service 实例
func cleanupSvcWithStorage(st storage.StorageInterface) *adminApp.Service {
	svc := adminApp.NewService()
	svc.SetStorage(st)
	return svc
}

// startExpiredFileCleanup 定时清理过期文件（DB 记录 + 物理文件）。
// 默认每 1 小时执行一次；懒清理由取件路径覆盖（GetFileByCode 发现过期即返回错误）。
func startExpiredFileCleanup(svc *adminApp.Service) {
	interval := time.Hour
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	ctx := context.Background()
	for range ticker.C {
		if n, freed, err := svc.CleanExpiredFiles(ctx); err != nil {
			logger.Error("expired file cleanup failed", zap.Error(err))
		} else if n > 0 {
			logger.Info("expired files cleaned",
				zap.Int64("count", n),
				zap.Int64("freed_bytes", freed))
		}
	}
}

// bootstrapBaseURL 对外基础地址（server.base_url 优先，否则 host:port）
func bootstrapBaseURL() string {
	if config.Server.BaseURL != "" {
		return config.Server.BaseURL
	}
	return fmt.Sprintf("http://%s:%d", config.Server.Host, config.Server.Port)
}

// bootstrapStorage 单例：share/chunk/presign/admin/清理共用同一实例，
// 管理端在线切换存储（Reload）才能对全部读写链路生效。
var (
	bootstrapStorageOnce sync.Once
	bootstrapStorageSvc  *storage.StorageService
)

// getBootstrapStorageService bootstrap 用的 storage 单例。
// 配置来自 conf（DB 持久化的 runtime_storage 已由 restoreRuntimeStorage 恢复进 conf）；
// 远端驱动构造失败时记录错误并降级 local（EffectiveType/InitError 可查真相）。
func getBootstrapStorageService() *storage.StorageService {
	bootstrapStorageOnce.Do(func() {
		cfg := storage.ConfigFromConf(&config.Storage, bootstrapBaseURL())
		svc, err := storage.NewStorageServiceE(cfg)
		if err != nil {
			logger.Error("remote storage backend init failed, fallback to local",
				zap.String("type", string(cfg.Type)), zap.Error(err))
			svc = storage.NewStorageService(cfg)
		} else if t := svc.EffectiveType(); t != storage.StorageTypeLocal {
			logger.Info("remote storage backend enabled", zap.String("type", string(t)))
		}
		bootstrapStorageSvc = svc
	})
	return bootstrapStorageSvc
}

// restoreRuntimeStorage 启动时把 system_configs.runtime_storage 恢复进全局配置。
// 优先级：env（FCB_STORAGE_TYPE/FCB_STORAGE_PATH）> DB（管理端在线修改的意图，
// 晚于 yaml）> yaml。DB 无记录时不动 conf（yaml/env 生效）。
func restoreRuntimeStorage() {
	rs := adminApp.NewService().LoadRuntimeStorage(context.Background())
	if rs == nil {
		return
	}
	if rs.Type != "" {
		config.Storage.Type = rs.Type
	}
	if rs.StoragePath != "" {
		config.Storage.StoragePath = rs.StoragePath
	}
	if rs.S3 != nil {
		config.Storage.S3 = rs.S3
	}
	if rs.WebDAV != nil {
		config.Storage.WebDAV = rs.WebDAV
	}
	// env 优先级最高：显式注入的环境变量覆盖 DB 恢复值
	if v := os.Getenv("FCB_STORAGE_TYPE"); v != "" {
		config.Storage.Type = v
	}
	if v := os.Getenv("FCB_STORAGE_PATH"); v != "" {
		config.Storage.StoragePath = v
	}
	log.Println("runtime storage config restored from database, type =", config.Storage.Type)
}
