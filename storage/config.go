// config.go 存储配置映射：StorageConfig 与 conf.StorageConfig 的互转（bootstrap 与管理端切换共用）。
package storage

import (
	"strings"

	"github.com/pigeonbox/core/conf"
)

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
	// Root 远端根目录（webdav：所有对象挂其下，避免绝对路径写入；空 = "pigeonbox"）
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
