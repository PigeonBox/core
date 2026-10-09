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

	"github.com/pigeonbox/core/storage/opendal"
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
			root = "pigeonbox"
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
			"root":     defaultRoot(cfg.FTP.Root, "pigeonbox"),
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
				"root":        defaultRoot(cfg.SFTP.Root, "pigeonbox"),
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
				"root":      defaultRoot(cfg.Azure.Root, "pigeonbox"),
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
				"root":     defaultRoot(cfg.HDFS.Root, "pigeonbox"),
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
				"root":          defaultRoot(cfg.OneDrive.Root, "pigeonbox"),
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

// boolPtr bool 取指针
func boolPtr(b bool) *bool { return &b }

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

// RemoteObjectInfo 远端对象列举项（presign 孤儿清理用）。
type RemoteObjectInfo struct {
	Key     string
	Size    int64
	ModTime time.Time
}

// ListRemoteObjects 列举远端后端 prefix 下对象（递归）。
// 仅远端后端支持；本地后端返回错误（本地对账走 ReconcileOrphans 磁盘遍历）。
func (s *StorageService) ListRemoteObjects(ctx context.Context, prefix string) ([]RemoteObjectInfo, error) {
	_, op := s.current()
	if op == nil {
		return nil, fmt.Errorf("存储未初始化")
	}
	mds, err := op.List(ctx, prefix)
	if err != nil {
		return nil, err
	}
	out := make([]RemoteObjectInfo, 0, len(mds))
	for _, md := range mds {
		if md == nil || md.IsDir {
			continue
		}
		out = append(out, RemoteObjectInfo{Key: md.Path, Size: md.Size, ModTime: md.ModTime})
	}
	return out, nil
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
