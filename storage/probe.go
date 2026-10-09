// probe.go 存储后端探测：构造 Operator 并做认证级验证（管理端「测试连接」/切换保存前调用）。
package storage

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"github.com/pigeonbox/core/storage/opendal"
)

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
		probe := filepath.Join(path, ".pb_probe")
		if err := os.WriteFile(probe, []byte("probe"), 0o644); err != nil {
			return fmt.Errorf("存储路径不可写: %w", err)
		}
		_ = os.Remove(probe)
		return nil
	}
}
