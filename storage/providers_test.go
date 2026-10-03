package storage

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestResolveCloudProvider_EndpointDerivation(t *testing.T) {
	cases := []struct {
		provider StorageType
		region   string
		want     string
	}{
		{StorageTypeOSS, "cn-hangzhou", "oss-cn-hangzhou.aliyuncs.com"},
		{StorageTypeCOS, "ap-guangzhou", "cos.ap-guangzhou.myqcloud.com"},
		{StorageTypeBOS, "bj", "s3.bj.bcebos.com"},
		{StorageTypeKS3, "BEIJING", "ks3.BEIJING.ksyuncs.com"},
		{StorageTypeOBS, "cn-north-4", "obs.cn-north-4.myhuaweicloud.com"},
	}
	for _, tc := range cases {
		opts, err := ResolveCloudProvider(tc.provider, tc.region, "bk", "ak", "sk", "", nil, nil)
		require.NoError(t, err, tc.provider)
		assert.Equal(t, tc.want, opts["endpoint"], tc.provider)
		assert.Equal(t, "false", opts["path_style"], tc.provider+" 应默认 virtual-host 风格")
		assert.Equal(t, "true", opts["use_ssl"], tc.provider+" 应默认 TLS")
	}
}

func TestResolveCloudProvider_AWSNeedsRegion(t *testing.T) {
	// AWS 无静态端点模板：显式 endpoint 可用
	opts, err := ResolveCloudProvider(StorageTypeS3, "us-west-2", "bk", "ak", "sk", "", nil, nil)
	require.NoError(t, err)
	assert.Equal(t, "s3.us-west-2.amazonaws.com", opts["endpoint"])

	// region 与 endpoint 均缺 → 报错
	_, err = ResolveCloudProvider(StorageTypeS3, "", "bk", "ak", "sk", "", nil, nil)
	assert.Error(t, err)
}

func TestResolveCloudProvider_ExplicitOverrides(t *testing.T) {
	// 显式 endpoint 覆盖推导；自建 MinIO 开 path_style + 关 TLS
	opts, err := ResolveCloudProvider(StorageTypeCOS, "ap-beijing", "bk", "ak", "sk",
		"cos.internal.example.com", boolPtrD(false), boolPtrD(true))
	require.NoError(t, err)
	assert.Equal(t, "cos.internal.example.com", opts["endpoint"])
	assert.Equal(t, "false", opts["use_ssl"])
	assert.Equal(t, "true", opts["path_style"])
}

func TestResolveCloudProvider_Validation(t *testing.T) {
	// 缺 AK/SK / 缺 bucket / 未知厂商
	_, err := ResolveCloudProvider(StorageTypeOSS, "cn-hangzhou", "bk", "", "sk", "", nil, nil)
	assert.Error(t, err)
	_, err = ResolveCloudProvider(StorageTypeOSS, "cn-hangzhou", "", "ak", "sk", "", nil, nil)
	assert.Error(t, err)
	_, err = ResolveCloudProvider("gcs", "x", "bk", "ak", "sk", "", nil, nil)
	assert.Error(t, err)
}

func TestBuildOperator_CloudProviders(t *testing.T) {
	// 各厂商配置经 buildOperator 构造成功（构造阶段不联网），
	// 且映射为 SchemeS3（presign 直传/直下随之生效）
	for _, provider := range []StorageType{StorageTypeOSS, StorageTypeCOS, StorageTypeBOS, StorageTypeKS3, StorageTypeOBS} {
		cfg := &StorageConfig{
			Type:      provider,
			Region:    "cn-hangzhou",
			Bucket:    "demo-bucket",
			AccessKey: "ak",
			SecretKey: "sk",
		}
		op, err := buildOperator(cfg)
		require.NoError(t, err, string(provider))
		require.NotNil(t, op)
		assert.Equal(t, "s3", string(op.Scheme()), string(provider)+" 应映射为 S3 兼容驱动")
	}

	// 配置缺失 → 明确报错（不静默回退本地盘——假开关防线）
	_, err := buildOperator(&StorageConfig{Type: StorageTypeCOS, Region: "ap-guangzhou"})
	assert.Error(t, err)
}

func boolPtrD(b bool) *bool { return &b }
