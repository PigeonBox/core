package storage

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/filescodebox/core/conf"
	"github.com/filescodebox/core/storage/opendal"
)

// ErrPresignUnsupported 当前存储后端不支持真预签名直传/直下（local/webdav 走服务端中转）。
var ErrPresignUnsupported = errors.New("presign direct transfer not supported by current storage backend")

// StorageType 存储类型
type StorageType string

const (
	StorageTypeLocal  StorageType = "local"
	StorageTypeS3     StorageType = "s3"
	StorageTypeWebDAV StorageType = "webdav"
	StorageTypeFTP    StorageType = "ftp"
	StorageTypeSFTP   StorageType = "sftp"
	StorageTypeGCS    StorageType = "gcs"
	StorageTypeAzBlob StorageType = "azureblob"
	StorageTypeHDFS   StorageType = "hdfs"
	StorageTypeOneDrv StorageType = "onedrive"
)

// FileOperationResult 文件操作结果
type FileOperationResult struct {
	Success   bool                   `json:"success"`
	Message   string                 `json:"message,omitempty"`
	Error     error                  `json:"-"`
	FilePath  string                 `json:"file_path,omitempty"`
	FileSize  int64                  `json:"file_size,omitempty"`
	FileHash  string                 `json:"file_hash,omitempty"`
	Metadata  map[string]interface{} `json:"metadata,omitempty"`
	Timestamp time.Time              `json:"timestamp"`
}

// StorageInterface 存储接口（简化版）
type StorageInterface interface {
	// 基础操作
	SaveFile(ctx context.Context, file *multipart.FileHeader, savePath string) (*FileOperationResult, error)
	DeleteFile(ctx context.Context, filePath string) error
	GetFile(ctx context.Context, filePath string) ([]byte, error)
	FileExists(ctx context.Context, filePath string) bool

	// 流式写入（本地导入/服务端拷贝用；返回写入字节数）
	SaveStream(ctx context.Context, savePath string, r io.Reader, expectedSize int64) (int64, error)

	// 分片操作
	SaveChunk(ctx context.Context, uploadID string, chunkIndex int, data []byte) error
	MergeChunks(ctx context.Context, uploadID string, totalChunks int, savePath string) error
	CleanChunks(ctx context.Context, uploadID string) error

	// 工具方法
	GetFileSize(ctx context.Context, filePath string) (int64, error)
	GetFileURL(ctx context.Context, filePath string) (string, error)

	// 流式下载方法
	GetFileReader(ctx context.Context, filePath string) (io.ReadCloser, int64, error)
}

// StorageConfig 存储配置
type StorageConfig struct {
	Type     StorageType
	DataPath string // 本地存储路径
	BaseURL  string // 基础URL

	// S3 配置
	Endpoint  string
	AccessKey string
	SecretKey string
	Bucket    string
	Region    string
	UseSSL    bool
	PathStyle bool // 路径风格寻址（MinIO/Ceph 自建场景）

	// WebDAV 配置
	WebDAVURL      string
	WebDAVUsername string
	WebDAVPassword string
	// Root 远端根目录（webdav：所有对象挂其下，避免绝对路径写入；空 = "filecodebox"）
	Root string

	// FTP/SFTP/Azure Blob/HDFS/OneDrive 配置（存储驱动扩展；指针复用 conf 结构）
	FTP      *conf.FTPConfig
	SFTP     *conf.SFTPConfig
	Azure    *conf.AzureBlobConfig
	HDFS     *conf.HDFSConfig
	OneDrive *conf.OneDriveConfig
}

// ConfigFromConf 把 conf 的存储配置映射为 StorageConfig（bootstrap 与管理端切换共用）。
func ConfigFromConf(c *conf.StorageConfig, baseURL string) *StorageConfig {
	if c == nil {
		return nil
	}
	cfg := &StorageConfig{
		Type:     StorageType(c.Type),
		DataPath: c.StoragePath,
		BaseURL:  baseURL,
	}
	if cfg.DataPath == "" {
		cfg.DataPath = "./data"
	}
	// 云厂商段归一：type 对应的专属段优先，回落到 s3 通用段（同 S3 兼容字段）
	cloud := c.S3
	switch StorageType(strings.ToLower(c.Type)) {
	case StorageTypeOSS:
		cloud = cloudFromCloudConfig(c.OSS, cloud)
	case StorageTypeCOS:
		cloud = cloudFromCloudConfig(c.COS, cloud)
	case StorageTypeBOS:
		cloud = cloudFromCloudConfig(c.BOS, cloud)
	case StorageTypeKS3:
		cloud = cloudFromCloudConfig(c.KS3, cloud)
	case StorageTypeOBS:
		cloud = cloudFromCloudConfig(c.OBS, cloud)
	}
	if cloud != nil {
		cfg.Endpoint = cloud.Endpoint
		cfg.Region = cloud.Region
		cfg.Bucket = cloud.Bucket
		cfg.AccessKey = cloud.AccessKey
		cfg.SecretKey = cloud.SecretKey
		cfg.UseSSL = cloud.UseSSL
		cfg.PathStyle = cloud.PathStyle
	}
	if c.WebDAV != nil {
		cfg.WebDAVURL = c.WebDAV.Endpoint
		cfg.WebDAVUsername = c.WebDAV.Username
		cfg.WebDAVPassword = c.WebDAV.Password
	}
	cfg.FTP = c.FTP
	cfg.SFTP = c.SFTP
	cfg.Azure = c.AzureBlob
	cfg.HDFS = c.HDFS
	cfg.OneDrive = c.OneDrive
	return cfg
}

// cloudFromCloudConfig 把厂商专属段转 S3 兼容形态（nil 时回落 fallback）。
// UseSSL 缺省 true（各云厂商公网端点均为 TLS）。
func cloudFromCloudConfig(cc *conf.CloudStorageConfig, fallback *conf.S3Config) *conf.S3Config {
	if cc == nil {
		return fallback
	}
	useSSL := true
	if cc.UseSSL != nil {
		useSSL = *cc.UseSSL
	}
	return &conf.S3Config{
		Endpoint:  cc.Endpoint,
		Region:    cc.Region,
		Bucket:    cc.Bucket,
		AccessKey: cc.AccessKey,
		SecretKey: cc.SecretKey,
		UseSSL:    useSSL,
	}
}

// ToConf 反向映射（运行时切换后持久化回 conf.StorageConfig 形态）。
func (c *StorageConfig) ToConf() *conf.StorageConfig {
	out := &conf.StorageConfig{
		Type:        string(c.Type),
		StoragePath: c.DataPath,
	}
	if c.Endpoint != "" || c.Bucket != "" {
		out.S3 = &conf.S3Config{
			Endpoint:  c.Endpoint,
			Region:    c.Region,
			Bucket:    c.Bucket,
			AccessKey: c.AccessKey,
			SecretKey: c.SecretKey,
			UseSSL:    c.UseSSL,
			PathStyle: c.PathStyle,
		}
	}
	if c.WebDAVURL != "" {
		out.WebDAV = &conf.WebDAVConfig{
			Endpoint: c.WebDAVURL,
			Username: c.WebDAVUsername,
			Password: c.WebDAVPassword,
		}
	}
	return out
}

// buildOperator 按配置类型构造远端 Operator；local 返回 (nil, nil)。
func buildOperator(cfg *StorageConfig) (*opendal.Operator, error) {
	switch cfg.Type {
	case StorageTypeS3:
		if cfg.Endpoint == "" || cfg.Bucket == "" || cfg.AccessKey == "" || cfg.SecretKey == "" {
			return nil, fmt.Errorf("s3 配置不完整：endpoint/bucket/access_key/secret_key 均必填")
		}
		return opendal.New(opendal.Config{
			Scheme: opendal.SchemeS3,
			Root:   cfg.Bucket,
			Options: map[string]string{
				"endpoint":   cfg.Endpoint,
				"access_key": cfg.AccessKey,
				"secret_key": cfg.SecretKey,
				"bucket":     cfg.Bucket,
				"region":     cfg.Region,
				"use_ssl":    strconv.FormatBool(cfg.UseSSL),
				"path_style": strconv.FormatBool(cfg.PathStyle),
			},
		})
	case StorageTypeOSS, StorageTypeCOS, StorageTypeBOS, StorageTypeKS3, StorageTypeOBS:
		// 云厂商：全部走 S3 兼容驱动（minio-go SigV4），endpoint 按厂商+region
		// 推导（显式配置优先）；映射为 SchemeS3 后 presign 直传/直下随之生效。
		// force_virtual_host：minio-go 的 Auto 对自定义端点退化 path-style，
		// 云厂商桶一律 virtual-host（COS ap-beijing 等对 path-style 直接拒绳）。
		opts, err := ResolveCloudProvider(cfg.Type, cfg.Region, cfg.Bucket,
			cfg.AccessKey, cfg.SecretKey, cfg.Endpoint,
			boolPtr(cfg.UseSSL), boolPtr(cfg.PathStyle))
		if err != nil {
			return nil, err
		}
		return opendal.New(opendal.Config{
			Scheme: opendal.SchemeS3,
			Root:   opts["bucket"],
			Options: map[string]string{
				"endpoint":           opts["endpoint"],
				"access_key":         opts["access_key"],
				"secret_key":         opts["secret_key"],
				"bucket":             opts["bucket"],
				"region":             opts["region"],
				"use_ssl":            opts["use_ssl"],
				"path_style":         opts["path_style"],
				"force_virtual_host": "true",
			},
		})
	case StorageTypeWebDAV:
		if cfg.WebDAVURL == "" {
			return nil, fmt.Errorf("webdav 配置不完整：url 必填")
		}
		// root 必传：所有对象挂远端子目录下（此前缺省导致 abs() 生成
		// "/uploads/..." 绝对路径，多数 WebDAV 服务端拒绝 MkdirAll）
		root := cfg.Root
		if root == "" {
			root = "filecodebox"
		}
		return opendal.New(opendal.Config{
			Scheme: opendal.SchemeWebDAV,
			Options: map[string]string{
				"url":      cfg.WebDAVURL,
				"root":     root,
				"username": cfg.WebDAVUsername,
				"password": cfg.WebDAVPassword,
			},
		})
	case StorageTypeFTP:
		if cfg.FTP == nil || cfg.FTP.Host == "" {
			return nil, fmt.Errorf("ftp 配置不完整：host 必填")
		}
		opts := map[string]string{
			"host":     cfg.FTP.Host,
			"username": cfg.FTP.Username,
			"password": cfg.FTP.Password,
			"tls":      cfg.FTP.TLS,
			"root":     defaultRoot(cfg.FTP.Root, "filecodebox"),
		}
		return opendal.New(opendal.Config{Scheme: opendal.SchemeFTP, Options: opts})
	case StorageTypeSFTP:
		if cfg.SFTP == nil || cfg.SFTP.Host == "" || cfg.SFTP.Username == "" {
			return nil, fmt.Errorf("sftp 配置不完整：host/username 必填")
		}
		return opendal.New(opendal.Config{
			Scheme: opendal.SchemeSFTP,
			Options: map[string]string{
				"host":        cfg.SFTP.Host,
				"username":    cfg.SFTP.Username,
				"password":    cfg.SFTP.Password,
				"private_key": cfg.SFTP.PrivateKey,
				"host_key":    cfg.SFTP.HostKey,
				"root":        defaultRoot(cfg.SFTP.Root, "filecodebox"),
			},
		})
	case StorageTypeAzBlob:
		if cfg.Azure == nil || cfg.Azure.Account == "" || cfg.Azure.Container == "" {
			return nil, fmt.Errorf("azureblob 配置不完整：account/container 必填")
		}
		return opendal.New(opendal.Config{
			Scheme: opendal.SchemeAzBlob,
			Options: map[string]string{
				"account":   cfg.Azure.Account,
				"container": cfg.Azure.Container,
				"key":       cfg.Azure.Key,
				"sas":       cfg.Azure.SAS,
				"endpoint":  cfg.Azure.Endpoint,
				"root":      defaultRoot(cfg.Azure.Root, "filecodebox"),
			},
		})
	case StorageTypeHDFS:
		if cfg.HDFS == nil || cfg.HDFS.Endpoint == "" {
			return nil, fmt.Errorf("hdfs 配置不完整：endpoint（WebHDFS 根地址）必填")
		}
		return opendal.New(opendal.Config{
			Scheme: opendal.SchemeHDFS,
			Options: map[string]string{
				"endpoint": cfg.HDFS.Endpoint,
				"user":     cfg.HDFS.User,
				"root":     defaultRoot(cfg.HDFS.Root, "filecodebox"),
			},
		})
	case StorageTypeOneDrv:
		if cfg.OneDrive == nil || cfg.OneDrive.ClientID == "" || cfg.OneDrive.RefreshToken == "" {
			return nil, fmt.Errorf("onedrive 配置不完整：client_id/refresh_token 必填")
		}
		return opendal.New(opendal.Config{
			Scheme: opendal.SchemeOneDrive,
			Options: map[string]string{
				"client_id":     cfg.OneDrive.ClientID,
				"client_secret": cfg.OneDrive.ClientSecret,
				"refresh_token": cfg.OneDrive.RefreshToken,
				"tenant":        cfg.OneDrive.Tenant,
				"drive_id":      cfg.OneDrive.DriveID,
				"root":          defaultRoot(cfg.OneDrive.Root, "filecodebox"),
			},
		})
	case StorageTypeGCS:
		// GCS 走其 S3 兼容 XML 端点（需 HMAC 密钥：GCS 控制台 Settings→Interoperability）
		// force_virtual_host 同云厂商分支（GCS 互操作端点同为 virtual-host 风格）
		opts, err := ResolveCloudProvider(StorageTypeGCS, cfg.Region, cfg.Bucket,
			cfg.AccessKey, cfg.SecretKey, cfg.Endpoint,
			boolPtr(cfg.UseSSL), boolPtr(cfg.PathStyle))
		if err != nil {
			return nil, err
		}
		return opendal.New(opendal.Config{
			Scheme: opendal.SchemeS3,
			Root:   opts["bucket"],
			Options: map[string]string{
				"endpoint":           opts["endpoint"],
				"access_key":         opts["access_key"],
				"secret_key":         opts["secret_key"],
				"bucket":             opts["bucket"],
				"region":             opts["region"],
				"use_ssl":            opts["use_ssl"],
				"path_style":         opts["path_style"],
				"force_virtual_host": "true",
			},
		})
	default:
		return nil, nil
	}
}

// defaultRoot 远端根目录缺省值
func defaultRoot(root, def string) string {
	if strings.TrimSpace(root) == "" {
		return def
	}
	return root
}

// BuildAndProbe 构造 Operator 并做认证级探测（管理端「测试连接」用）。
// local 返回 nil operator（无需探测）。
func BuildAndProbe(ctx context.Context, cfg *StorageConfig) error {
	op, err := buildOperator(cfg)
	if err != nil {
		return err
	}
	if op == nil {
		return nil
	}
	return opendal.Probe(ctx, op)
}

// ProbeConfig 认证级验证存储配置（管理端切换/保存前调用）。
// s3/webdav：凭据+可达性；云厂商（oss/cos/bos/ks3/obs）：凭据有效且桶存在；
// local：路径可创建可写。
// 注意：云厂商类型必须显式列入下方 case——ConfigFromConf 归一后 Type 仍是
// 厂商名（cos 等），若落入 default 会被当 local 只探本地路径，坏配置静默上线
// （2026-10-05 215 实测：不存在的桶 Probe 照样通过，上传全挂）。
func ProbeConfig(ctx context.Context, cfg *StorageConfig) error {
	switch cfg.Type {
	case StorageTypeS3, StorageTypeWebDAV, StorageTypeFTP, StorageTypeSFTP, StorageTypeGCS,
		StorageTypeAzBlob, StorageTypeHDFS, StorageTypeOneDrv,
		StorageTypeOSS, StorageTypeCOS, StorageTypeBOS, StorageTypeKS3, StorageTypeOBS:
		op, err := buildOperator(cfg)
		if err != nil {
			return err
		}
		return opendal.Probe(ctx, op)
	default:
		path := cfg.DataPath
		if path == "" {
			return fmt.Errorf("存储路径未配置")
		}
		if err := os.MkdirAll(path, 0o755); err != nil {
			return fmt.Errorf("存储路径不可创建: %w", err)
		}
		probe := filepath.Join(path, ".fcb_probe")
		if err := os.WriteFile(probe, []byte("probe"), 0o644); err != nil {
			return fmt.Errorf("存储路径不可写: %w", err)
		}
		_ = os.Remove(probe)
		return nil
	}
}

// StorageService 存储服务。
// local 走本地文件系统（实现与拆分前完全一致）；s3/webdav 经 opendal.Operator
// 分派到对应驱动。管理端在线切换后端时通过 Reload 热替换（在途请求持旧后端完成）。
type StorageService struct {
	mu      sync.RWMutex
	config  *StorageConfig
	op      *opendal.Operator // local 时为 nil
	initErr error             // remote 构造失败降级 local 的原因
}

// NewStorageServiceE 创建存储服务；远端后端配置非法时返回错误（bootstrap 用，
// 失败要显式暴露而不是静默落本地盘）。
func NewStorageServiceE(config *StorageConfig) (*StorageService, error) {
	op, err := buildOperator(config)
	if err != nil {
		return nil, err
	}
	return &StorageService{config: config, op: op}, nil
}

// NewStorageService 创建存储服务（兼容入口）。
// 远端后端配置非法时降级 local 并记录原因（InitError 可查），与拆分前行为兼容。
func NewStorageService(config *StorageConfig) *StorageService {
	s, err := NewStorageServiceE(config)
	if err != nil {
		local := *config
		local.Type = StorageTypeLocal
		return &StorageService{config: &local, initErr: err}
	}
	return s
}

// Reload 热切换存储后端（管理端切换存储时调用）。构造失败时保留原后端并返回错误。
func (s *StorageService) Reload(config *StorageConfig) error {
	op, err := buildOperator(config)
	if err != nil {
		return fmt.Errorf("构建存储驱动失败: %w", err)
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.config, s.op, s.initErr = config, op, nil
	return nil
}

// DataPath 返回本地存储根目录（分片临时区；远端模式下仍承载合并副本）。
func (s *StorageService) DataPath() string {
	return s.config.DataPath
}

// EffectiveType 实际生效的存储类型（远端构造失败降级 local 时如实返回 local）。
func (s *StorageService) EffectiveType() StorageType {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if s.op == nil {
		return StorageTypeLocal
	}
	return s.config.Type
}

// InitError 返回构造降级原因（无降级时为 nil）。
func (s *StorageService) InitError() error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.initErr
}

// current 取当前配置与 Operator（远端时非 nil）。
func (s *StorageService) current() (*StorageConfig, *opendal.Operator) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.config, s.op
}

// chunkKey 分片对象 key（fs/远端统一 '/' 分隔；fs 侧 filepath.Join 兼容处理）
func chunkKey(uploadID string, index int) string {
	return "chunks/" + uploadID + fmt.Sprintf("/chunk_%d", index)
}

// SaveFile 保存文件。
// 落盘的同时用 io.TeeReader 流式计算 SHA-256（不额外占用整文件内存），
// 哈希写入 result.FileHash——这是秒传（按哈希秒传）与完整性的地基。
func (s *StorageService) SaveFile(ctx context.Context, file *multipart.FileHeader, savePath string) (*FileOperationResult, error) {
	startTime := time.Now()
	cfg, op := s.current()

	src, err := file.Open()
	if err != nil {
		return nil, fmt.Errorf("打开上传文件失败: %w", err)
	}
	defer func() { _ = src.Close() }()

	hasher := sha256.New()
	tee := io.TeeReader(src, hasher)
	var written int64

	if op != nil {
		// 远端：multipart header 自带大小，PutObject 类接口按 size 流式上传
		if err := op.WriteStream(ctx, savePath, tee, file.Size); err != nil {
			return nil, fmt.Errorf("保存文件失败: %w", err)
		}
		written = file.Size
	} else {
		fullPath := filepath.Join(cfg.DataPath, savePath)
		if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
			return nil, fmt.Errorf("创建目录失败: %w", err)
		}
		dst, err := os.Create(fullPath)
		if err != nil {
			return nil, fmt.Errorf("创建目标文件失败: %w", err)
		}
		defer func() { _ = dst.Close() }()
		written, err = io.Copy(dst, tee)
		if err != nil {
			return nil, fmt.Errorf("保存文件失败: %w", err)
		}
	}

	return &FileOperationResult{
		Success:   true,
		Message:   "文件保存成功",
		FilePath:  savePath,
		FileSize:  written,
		FileHash:  hex.EncodeToString(hasher.Sum(nil)),
		Timestamp: startTime,
	}, nil
}

// SaveStream 流式写入 reader 到相对路径（presign 中转大文件使用，避免整文件进内存）。
// expectedSize > 0 时远端驱动按确切 size 写入；返回实际写入字节数。
// 路径防御与 SaveBytes 一致（相对 DataPath，逃逸即拒绝）。
func (s *StorageService) SaveStream(ctx context.Context, savePath string, r io.Reader, expectedSize int64) (int64, error) {
	cfg, op := s.current()
	root := filepath.Clean(cfg.DataPath)
	clean := filepath.Clean(filepath.Join(root, savePath))
	rel, err := filepath.Rel(root, clean)
	if err != nil || clean == root || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return 0, fmt.Errorf("illegal save path")
	}
	if op != nil {
		if expectedSize <= 0 {
			return 0, fmt.Errorf("remote backend requires exact size")
		}
		if err := op.WriteStream(ctx, savePath, r, expectedSize); err != nil {
			return 0, err
		}
		return expectedSize, nil
	}
	fullPath := filepath.Join(root, savePath)
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		return 0, err
	}
	dst, err := os.Create(fullPath)
	if err != nil {
		return 0, err
	}
	defer func() { _ = dst.Close() }()
	return io.Copy(dst, r)
}

// SaveBytes 写入内存数据到相对路径（presign 直传使用）。
// 路径防御收敛在此处：相对 DataPath 计算清洗后的偏移，逃逸（../）即拒绝。
func (s *StorageService) SaveBytes(ctx context.Context, savePath string, data []byte) error {
	cfg, op := s.current()
	root := filepath.Clean(cfg.DataPath)
	clean := filepath.Clean(filepath.Join(root, savePath))
	rel, err := filepath.Rel(root, clean)
	if err != nil || clean == root || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return fmt.Errorf("illegal save path: %s", savePath)
	}
	if op != nil {
		return op.Write(ctx, savePath, data)
	}
	if err := os.MkdirAll(filepath.Dir(clean), 0o755); err != nil {
		return fmt.Errorf("create dir failed: %w", err)
	}
	return os.WriteFile(clean, data, 0o644)
}

// localContain 本地后端路径收口：rel 清洗后必须仍落在 DataPath 根内，返回绝对路径。
// 2026-10-05 审计修复：此前仅写侧（SaveStream/SaveBytes）有逃逸守卫，读/删/Stat
// 侧裸 Join——DB 中 RelPath 被投毒（如 multi-bind 的 object_key）即可任意读/删/
// Stat 服务器文件。所有本地路径访问统一经此。
func (s *StorageService) localContain(rel string) (string, error) {
	root := filepath.Clean(s.dataPath())
	full := filepath.Clean(filepath.Join(root, rel))
	r, err := filepath.Rel(root, full)
	if err != nil || full == root || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("illegal storage path: %s", rel)
	}
	return full, nil
}

// DeleteFile 删除文件
func (s *StorageService) DeleteFile(ctx context.Context, filePath string) error {
	_, op := s.current()
	if !s.FileExists(ctx, filePath) {
		return fmt.Errorf("文件不存在")
	}
	if op != nil {
		return op.Delete(ctx, filePath)
	}
	fullPath, err := s.localContain(filePath)
	if err != nil {
		return err
	}
	return os.Remove(fullPath)
}

// GetFile 获取文件内容
func (s *StorageService) GetFile(ctx context.Context, filePath string) ([]byte, error) {
	_, op := s.current()
	if op != nil {
		return op.Read(ctx, filePath)
	}
	fullPath, err := s.localContain(filePath)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(fullPath)
}

// FileExists 检查文件是否存在
func (s *StorageService) FileExists(ctx context.Context, filePath string) bool {
	_, op := s.current()
	if op != nil {
		return op.Exists(ctx, filePath)
	}
	fullPath, err := s.localContain(filePath)
	if err != nil {
		return false
	}
	_, err = os.Stat(fullPath)
	return !os.IsNotExist(err)
}

// SaveChunk 保存分片
func (s *StorageService) SaveChunk(ctx context.Context, uploadID string, chunkIndex int, data []byte) error {
	_, op := s.current()
	key := chunkKey(uploadID, chunkIndex)
	if op != nil {
		return op.Write(ctx, key, data)
	}
	chunkPath, err := s.localContain(key)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(chunkPath), 0755); err != nil {
		return err
	}
	return os.WriteFile(chunkPath, data, 0644)
}

// MergeChunks 合并分片。
// local：顺序拼接落盘；远端：懒打开分片的链式读器 + 按总大小流式上传，
// 不整文件进内存。
func (s *StorageService) MergeChunks(ctx context.Context, uploadID string, totalChunks int, savePath string) error {
	_, op := s.current()

	if op != nil {
		total := int64(0)
		for i := 0; i < totalChunks; i++ {
			md, err := op.Stat(ctx, chunkKey(uploadID, i))
			if err != nil {
				return fmt.Errorf("定位分片 %d 失败: %w", i, err)
			}
			total += md.Size
		}
		reader := &chunkChainReader{ctx: ctx, op: op, uploadID: uploadID, n: totalChunks}
		defer func() { _ = reader.Close() }()
		if err := op.WriteStream(ctx, savePath, reader, total); err != nil {
			return fmt.Errorf("合并分片失败: %w", err)
		}
		go func() { _ = s.CleanChunks(context.Background(), uploadID) }()
		return nil
	}

	fullPath, err := s.localContain(savePath)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(fullPath), 0755); err != nil {
		return err
	}
	dst, err := os.Create(fullPath)
	if err != nil {
		return err
	}
	defer func() { _ = dst.Close() }()

	for i := 0; i < totalChunks; i++ {
		chunkPath, err := s.localContain(chunkKey(uploadID, i))
		if err != nil {
			return fmt.Errorf("读取分片 %d 失败: %w", i, err)
		}
		chunkData, err := os.ReadFile(chunkPath)
		if err != nil {
			return fmt.Errorf("读取分片 %d 失败: %w", i, err)
		}
		if _, err := dst.Write(chunkData); err != nil {
			return fmt.Errorf("写入分片 %d 失败: %w", i, err)
		}
	}

	go func() { _ = s.CleanChunks(context.Background(), uploadID) }()
	return nil
}

// CleanChunks 清理分片
func (s *StorageService) CleanChunks(ctx context.Context, uploadID string) error {
	_, op := s.current()
	prefix := "chunks/" + uploadID
	if op != nil {
		return op.RemoveAll(ctx, prefix)
	}
	fullPath, err := s.localContain(prefix)
	if err != nil {
		return err
	}
	return os.RemoveAll(fullPath)
}

// boolPtr bool 取指针
func boolPtr(b bool) *bool { return &b }

// resolveLocal 定位本地文件：优先 DataPath+rel，失败回退 DataPath+/uploads/+rel。
// 兼容统一存储实例前的历史双层布局（chunk/share 懒加载单例的 DataPath 分别为
// ./data 与 ./data/uploads，直传历史文件落在 data/uploads/uploads/<rel>）。
// 两条候选路径均经 localContain 收口，逃逸（../）一律视为不存在。
func (s *StorageService) resolveLocal(rel string) (string, bool) {
	p, err := s.localContain(rel)
	if err != nil {
		return "", false
	}
	if _, err := os.Stat(p); err == nil {
		return p, true
	}
	alt, err := s.localContain(filepath.Join("uploads", rel))
	if err != nil {
		return p, false
	}
	if _, err := os.Stat(alt); err == nil {
		return alt, true
	}
	return p, false
}

// LocalAbsPath 本地后端下解析存储相对路径为绝对路径（仅 local 后端；远端返回空）。
// 供下载链路走 c.File（原生 Range/断点续传/MIME 推断）；路径不存在返回空。
func (s *StorageService) LocalAbsPath(rel string) string {
	if s.EffectiveType() != StorageTypeLocal {
		return ""
	}
	p, _ := s.resolveLocal(rel)
	if p == "" {
		return ""
	}
	if info, err := os.Stat(p); err != nil || info.IsDir() {
		return ""
	}
	return p
}

// GetFileSize 获取文件大小
func (s *StorageService) GetFileSize(ctx context.Context, filePath string) (int64, error) {
	_, op := s.current()
	if op != nil {
		md, err := op.Stat(ctx, filePath)
		if err != nil {
			return 0, err
		}
		return md.Size, nil
	}
	fullPath, err := s.localContain(filePath)
	if err != nil {
		return 0, err
	}
	info, err := os.Stat(fullPath)
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

// GetFileURL 获取文件URL
func (s *StorageService) GetFileURL(ctx context.Context, filePath string) (string, error) {
	cfg, _ := s.current()
	if cfg.BaseURL == "" {
		return "", fmt.Errorf("base URL not configured")
	}
	return fmt.Sprintf("%s/files/%s", cfg.BaseURL, filePath), nil
}

// GetFileReader 获取文件读取器（用于流式下载）
func (s *StorageService) GetFileReader(ctx context.Context, filePath string) (io.ReadCloser, int64, error) {
	_, op := s.current()
	if op != nil {
		md, err := op.Stat(ctx, filePath)
		if err != nil {
			return nil, 0, fmt.Errorf("文件不存在: %w", err)
		}
		rc, err := op.Reader(ctx, filePath)
		if err != nil {
			return nil, 0, fmt.Errorf("打开文件失败: %w", err)
		}
		return rc, md.Size, nil
	}
	fullPath, ok := s.resolveLocal(filePath)
	if !ok {
		return nil, 0, fmt.Errorf("文件不存在: %s", filePath)
	}
	fileInfo, err := os.Stat(fullPath)
	if err != nil {
		return nil, 0, fmt.Errorf("文件不存在: %w", err)
	}
	file, err := os.Open(fullPath)
	if err != nil {
		return nil, 0, fmt.Errorf("打开文件失败: %w", err)
	}
	return file, fileInfo.Size(), nil
}

// ErrRangeUnsupported 当前存储后端不支持按字节区间读取（Range 下载/断点续传）。
// 转发 opendal.ErrRangeUnsupported，调用方用 errors.Is 判断后回退全量流。
var ErrRangeUnsupported = opendal.ErrRangeUnsupported

// StatFile 获取单个文件大小（Range 下载等需要先知总大小的场景）。
func (s *StorageService) StatFile(ctx context.Context, filePath string) (int64, error) {
	_, op := s.current()
	if op != nil {
		md, err := op.Stat(ctx, filePath)
		if err != nil {
			return 0, fmt.Errorf("文件不存在: %w", err)
		}
		return md.Size, nil
	}
	fullPath, ok := s.resolveLocal(filePath)
	if !ok {
		return 0, fmt.Errorf("文件不存在: %s", filePath)
	}
	info, err := os.Stat(fullPath)
	if err != nil {
		return 0, fmt.Errorf("文件不存在: %w", err)
	}
	return info.Size(), nil
}

// GetFileReaderRange 区间读取文件（Range 下载/断点续传）。
// 返回 (区间读器, 对象总大小)。当前后端不支持区间读时返回
// opendal.ErrRangeUnsupported，调用方应回退全量流式（200）。
// start 越界（>= 文件大小）返回 os.ErrNotExist 语义错误（调用方 416）。
func (s *StorageService) GetFileReaderRange(ctx context.Context, filePath string, start, length int64) (io.ReadCloser, int64, error) {
	_, op := s.current()
	if op != nil {
		md, err := op.Stat(ctx, filePath)
		if err != nil {
			return nil, 0, fmt.Errorf("文件不存在: %w", err)
		}
		if start >= md.Size {
			return nil, md.Size, fmt.Errorf("区间起点越界: %w", os.ErrNotExist)
		}
		rc, err := op.ReadRange(ctx, filePath, start, length)
		if err != nil {
			return nil, md.Size, err
		}
		return rc, md.Size, nil
	}
	fullPath, ok := s.resolveLocal(filePath)
	if !ok {
		return nil, 0, fmt.Errorf("文件不存在: %s", filePath)
	}
	info, err := os.Stat(fullPath)
	if err != nil {
		return nil, 0, fmt.Errorf("文件不存在: %w", err)
	}
	if start >= info.Size() {
		return nil, info.Size(), fmt.Errorf("区间起点越界: %w", os.ErrNotExist)
	}
	f, err := os.Open(fullPath)
	if err != nil {
		return nil, info.Size(), fmt.Errorf("打开文件失败: %w", err)
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		_ = f.Close()
		return nil, info.Size(), fmt.Errorf("定位区间失败: %w", err)
	}
	if length > 0 {
		return struct {
			io.Reader
			io.Closer
		}{io.LimitReader(f, length), f}, info.Size(), nil
	}
	return f, info.Size(), nil
}

// GenerateFilePath 已删除：零调用方（上传路径统一走 pkg/utils.NewUploadRelPath）。

// StoredFileEntry 已落存储的文件项（多文件分享/访客投递等跨域流转的统一载体）。
// 语义约定：传输层保证物理文件已就位（直传保存/分片合并/presign 核实），
// 业务层只做装配。定义在本包使 request 等域引用它时无需依赖具体业务域。
type StoredFileEntry struct {
	// RelPath 存储相对路径（含唯一文件名；local 下与合并写入路径一致）
	RelPath string
	// FileName 原始文件名（消毒后，仅展示）
	FileName string
	// Size 实际大小（handler 已复核）
	Size int64
	// FileHash SHA-256（可空：哈希失败不阻断分享）
	FileHash string
}

// dataPath 读取当前 DataPath（local 分支内部使用）
func (s *StorageService) dataPath() string {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.config.DataPath
}

// PresignPutURL 生成真预签名直传 PUT URL（客户端直传对象存储，服务器不过流量）。
// 仅 s3 等支持离线签名的后端可用；local/webdav 返回 ErrPresignUnsupported，
// 调用方（presign 域）据此回退自家中转。
func (s *StorageService) PresignPutURL(ctx context.Context, objectKey string, expire time.Duration) (string, error) {
	return s.presignURL(ctx, objectKey, "PUT", expire)
}

// PresignGetURL 生成真预签名直下 GET URL（短时效，下载 302 用）。
func (s *StorageService) PresignGetURL(ctx context.Context, objectKey string, expire time.Duration) (string, error) {
	return s.presignURL(ctx, objectKey, "GET", expire)
}

func (s *StorageService) presignURL(ctx context.Context, objectKey, method string, expire time.Duration) (string, error) {
	_, op := s.current()
	if op == nil || op.Scheme() != opendal.SchemeS3 {
		return "", ErrPresignUnsupported
	}
	res, err := op.Presign(ctx, opendal.PresignedRequest{Path: objectKey, Method: method, Expire: expire})
	if err != nil {
		return "", err
	}
	return res.URL, nil
}

// HeadObject 返回对象大小与 ETag（s3 直传 Complete 时的存在性/大小核实）。
func (s *StorageService) HeadObject(ctx context.Context, objectKey string) (int64, string, error) {
	_, op := s.current()
	if op == nil {
		info, err := os.Stat(filepath.Join(s.dataPath(), objectKey))
		if err != nil {
			return 0, "", err
		}
		return info.Size(), "", nil
	}
	md, err := op.Stat(ctx, objectKey)
	if err != nil {
		return 0, "", err
	}
	return md.Size, md.ETag, nil
}

// chunkChainReader 远端合并分片用的顺序读器：按 chunk_0..chunk_{n-1} 懒打开，
// 读完一片自动切下一片，避免整文件进内存。
type chunkChainReader struct {
	ctx      context.Context
	op       *opendal.Operator
	uploadID string
	i, n     int
	cur      io.ReadCloser
}

func (r *chunkChainReader) Read(p []byte) (int, error) {
	for {
		if r.cur == nil {
			if r.i >= r.n {
				return 0, io.EOF
			}
			rc, err := r.op.Reader(r.ctx, chunkKey(r.uploadID, r.i))
			if err != nil {
				return 0, fmt.Errorf("读取分片 %d 失败: %w", r.i, err)
			}
			r.cur = rc
		}
		n, err := r.cur.Read(p)
		if err == io.EOF {
			// Read 允许数据与 EOF 同帧返回（(n>0, io.EOF)）：必须先交付这批
			// 字节、下轮 Read 再切下一片。此前直接丢弃 n，合并产物在
			// 「末段+EOF 同帧」场景静默缺尾（COS 实测 100000 字节分片只合
			// 并出 98304，Put 报 Body length 与 ContentLength 不符）。
			if n > 0 {
				return n, nil
			}
			_ = r.cur.Close()
			r.cur = nil
			r.i++
			continue
		}
		if err != nil {
			return n, err
		}
		if n > 0 {
			return n, nil
		}
	}
}

func (r *chunkChainReader) Close() error {
	if r.cur != nil {
		_ = r.cur.Close()
		r.cur = nil
	}
	return nil
}
