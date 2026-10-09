// backends.go 存储后端注册表：类型 → Operator 构建器。
//
// 此前 buildOperator 是 14 后端巨型 switch，ProbeConfig 另持一份"远端类型"
// case 清单——新增后端须同步改两处，漏列 ProbeConfig 的坏配置会静默按 local
// 探测通过（2026-10-05 215 实测事故：不存在的桶 Probe 照样通过，上传全挂）。
// 现收口为单一注册表：ProbeConfig/BuildAndProbe 的远端判定直接由注册表派生，
// 未注册且非 local 的类型 fail-loud（未知类型不再静默当 local）。
//
// 新增后端只需：① 本表注册构建器；② opendal 子包实现 Scheme 驱动；
// ③ StorageConfig/ConfigFromConf 补配置段。探测链路自动生效。
package storage

import (
	"fmt"
	"strconv"

	"github.com/pigeonbox/core/storage/opendal"
)

// backendBuilder 从存储配置构造远端 Operator（配置校验失败返回 error）。
type backendBuilder func(cfg *StorageConfig) (*opendal.Operator, error)

// backendBuilders 远端后端注册表。local 有意不注册——它无远端 Operator
// （buildOperator 对 local/空类型返回 (nil, nil)，StorageService 走本地文件系统分支）。
var backendBuilders = map[StorageType]backendBuilder{
	StorageTypeS3:     buildS3Operator,
	StorageTypeOSS:    buildCloudS3Operator,
	StorageTypeCOS:    buildCloudS3Operator,
	StorageTypeBOS:    buildCloudS3Operator,
	StorageTypeKS3:    buildCloudS3Operator,
	StorageTypeOBS:    buildCloudS3Operator,
	StorageTypeGCS:    buildCloudS3Operator,
	StorageTypeWebDAV: buildWebDAVOperator,
	StorageTypeFTP:    buildFTPOperator,
	StorageTypeSFTP:   buildSFTPOperator,
	StorageTypeAzBlob: buildAzBlobOperator,
	StorageTypeHDFS:   buildHDFSOperator,
	StorageTypeOneDrv: buildOneDriveOperator,
}

// isRemoteBackend 类型是否注册了远端构建器（ProbeConfig/BuildAndProbe 的
// 远端/本地分流判据，取代旧手写 case 清单）。
func isRemoteBackend(t StorageType) bool {
	_, ok := backendBuilders[t]
	return ok
}

// isLocalBackend 类型是否按本地文件系统处理（空类型与显式 local 等价，
// 沿用历史 default 分支语义）。
func isLocalBackend(t StorageType) bool {
	return t == "" || t == StorageTypeLocal
}

func buildS3Operator(cfg *StorageConfig) (*opendal.Operator, error) {
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
}

// buildCloudS3Operator 云厂商（oss/cos/bos/ks3/obs/gcs）：全部走 S3 兼容驱动
// （minio-go SigV4），endpoint 按厂商+region 推导（显式配置优先）；映射为
// SchemeS3 后 presign 直传/直下随之生效。
// force_virtual_host：minio-go 的 Auto 对自定义端点退化 path-style，
// 云厂商桶一律 virtual-host（COS ap-beijing 等对 path-style 直接拒绳）。
// GCS 走其 S3 兼容 XML 端点（需 HMAC 密钥：GCS 控制台 Settings→Interoperability），
// 互操作端点同为 virtual-host 风格，与其余厂商共用本构建器（cfg.Type 即厂商名）。
func buildCloudS3Operator(cfg *StorageConfig) (*opendal.Operator, error) {
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
}

func buildWebDAVOperator(cfg *StorageConfig) (*opendal.Operator, error) {
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
}

func buildFTPOperator(cfg *StorageConfig) (*opendal.Operator, error) {
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
}

func buildSFTPOperator(cfg *StorageConfig) (*opendal.Operator, error) {
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
}

func buildAzBlobOperator(cfg *StorageConfig) (*opendal.Operator, error) {
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
}

func buildHDFSOperator(cfg *StorageConfig) (*opendal.Operator, error) {
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
}

func buildOneDriveOperator(cfg *StorageConfig) (*opendal.Operator, error) {
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
}
