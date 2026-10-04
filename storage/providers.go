// providers.go 云厂商对象存储预设（S3 兼容协议）。
//
// 腾讯云 COS / 阿里云 OSS / 百度云 BOS / 金山云 KS3 / 华为云 OBS / AWS S3
// 均提供 S3 兼容端点，统一经 s3 驱动（minio-go SigV4）读写；
// 本表负责三件事：
//  1. endpoint 推导：用户只填 region（endpoint 留空时按厂商模板生成）
//  2. path_style 缺省：各厂商均为 virtual-host 风格（MinIO/自建才用 path style）
//  3. type 白名单与展示名
package storage

import (
	"fmt"
	"strings"
)

// ProviderPreset 厂商预设
type ProviderPreset struct {
	// DeriveEndpoint 按 region 推导 S3 兼容端点（不带协议前缀；返回空表示必须显式配置）
	DeriveEndpoint func(region string) string
	// DefaultPathStyle 各厂商均为 virtual-host；MinIO/自建 S3 才默认 path style
	DefaultPathStyle bool
	// DisplayName 运维展示名
	DisplayName string
}

const (
	StorageTypeOSS = StorageType("oss")
	StorageTypeCOS = StorageType("cos")
	StorageTypeBOS = StorageType("bos")
	StorageTypeKS3 = StorageType("ks3")
	StorageTypeOBS = StorageType("obs")
)

// providerPresets 厂商预设表（key = storage.type 小写）
var providerPresets = map[StorageType]ProviderPreset{
	// AWS S3：region 必填（端点由 minio-go 按签名区域解析）
	StorageTypeS3: {
		DeriveEndpoint:   func(region string) string { return "" },
		DefaultPathStyle: false,
		DisplayName:      "Amazon S3",
	},
	// 阿里云 OSS：oss-<region>.aliyuncs.com（region 如 cn-hangzhou）
	StorageTypeOSS: {
		DeriveEndpoint: func(region string) string {
			if region == "" {
				return ""
			}
			return fmt.Sprintf("oss-%s.aliyuncs.com", region)
		},
		DisplayName: "阿里云 OSS",
	},
	// 腾讯云 COS：cos.<region>.myqcloud.com（region 如 ap-guangzhou）
	StorageTypeCOS: {
		DeriveEndpoint: func(region string) string {
			if region == "" {
				return ""
			}
			return fmt.Sprintf("cos.%s.myqcloud.com", region)
		},
		DisplayName: "腾讯云 COS",
	},
	// 百度云 BOS：S3 兼容端点 s3.<region>.bcebos.com（region 如 bj）
	StorageTypeBOS: {
		DeriveEndpoint: func(region string) string {
			if region == "" {
				return ""
			}
			return fmt.Sprintf("s3.%s.bcebos.com", region)
		},
		DisplayName: "百度云 BOS",
	},
	// 金山云 KS3：ks3.<region>.ksyuncs.com（region 如 BEIJING）
	StorageTypeKS3: {
		DeriveEndpoint: func(region string) string {
			if region == "" {
				return ""
			}
			return fmt.Sprintf("ks3.%s.ksyuncs.com", region)
		},
		DisplayName: "金山云 KS3",
	},
	// 华为云 OBS：obs.<region>.myhuaweicloud.com（region 如 cn-north-4）
	StorageTypeOBS: {
		DeriveEndpoint: func(region string) string {
			if region == "" {
				return ""
			}
			return fmt.Sprintf("obs.%s.myhuaweicloud.com", region)
		},
		DisplayName: "华为云 OBS",
	},
	// Google Cloud Storage：S3 兼容 XML 端点 storage.googleapis.com（固定，region 无关；
	// 需在 GCS 控制台 Settings→Interoperability 启用 HMAC 密钥作为 access/secret）。
	// virtual-host 与 path-style 均支持；presign 直传直下随 S3 驱动生效。
	StorageTypeGCS: {
		DeriveEndpoint:   func(region string) string { return "storage.googleapis.com" },
		DefaultPathStyle: false,
		DisplayName:      "Google Cloud Storage",
	},
}

// IsCloudProviderType type 是否为受支持的云厂商（S3 兼容）
func IsCloudProviderType(t StorageType) bool {
	_, ok := providerPresets[t]
	return ok
}

// ResolveCloudProvider 把厂商配置归一为 S3 驱动 options。
// 返回 (options, error)；endpoint 由显式配置或 region 推导得出。
func ResolveCloudProvider(t StorageType, region, bucket, accessKey, secretKey, endpoint string, useSSL, pathStyle *bool) (map[string]string, error) {
	preset, ok := providerPresets[t]
	if !ok {
		return nil, fmt.Errorf("不支持的云厂商类型: %s", t)
	}
	if bucket == "" {
		return nil, fmt.Errorf("%s: bucket 必填", t)
	}
	if accessKey == "" || secretKey == "" {
		return nil, fmt.Errorf("%s: access_key/secret_key 必填", t)
	}

	ep := strings.TrimSpace(endpoint)
	if ep == "" {
		// 端点可由厂商预设推导（GCS 固定端点 region 无关；其余按 region 模板；
		// AWS S3 模板返回空 → 走下方 region 默认端点）
		ep = preset.DeriveEndpoint(region)
		if ep == "" {
			if region == "" {
				return nil, fmt.Errorf("%s: region 与 endpoint 至少配置一项", t)
			}
			// AWS S3：无静态端点模板，region 交给 minio-go 签名解析
			ep = fmt.Sprintf("s3.%s.amazonaws.com", region)
		}
	}

	ssl := true
	if useSSL != nil {
		ssl = *useSSL
	}
	ps := preset.DefaultPathStyle
	if pathStyle != nil {
		ps = *pathStyle
	}

	return map[string]string{
		"endpoint":   ep,
		"bucket":     bucket,
		"access_key": accessKey,
		"secret_key": secretKey,
		"region":     region,
		"use_ssl":    fmt.Sprintf("%t", ssl),
		"path_style": fmt.Sprintf("%t", ps),
	}, nil
}
