package security

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestDownloadTokenRoundTrip(t *testing.T) {
	SetDownloadTokenSecret("test-secret-32-chars-xxxxxxxxxxxxx")
	code := "AbCd1234"

	token := GenerateDownloadToken(code)
	require.NotEmpty(t, token)
	assert.True(t, VerifyDownloadToken(code, token), "正确 code+token 应通过")

	// 换 code 校验失败（令牌绑定分享码）
	assert.False(t, VerifyDownloadToken("other123", token))
	// 篡改签名失败
	assert.False(t, VerifyDownloadToken(code, token[:len(token)-2]+"xx"))
	// 格式错误
	assert.False(t, VerifyDownloadToken(code, "not-a-token"))
	assert.False(t, VerifyDownloadToken(code, "abc.def"))
	// 过期时间戳
	exp := time.Now().Add(-time.Hour).Unix()
	forged := strings.Split(token, ".")[1]
	assert.False(t, VerifyDownloadToken(code, "123."+forged))
	_ = exp
}

func TestDownloadTokenUnsigned(t *testing.T) {
	// 未注入密钥：生成返回空串，校验恒 false（调用方按未启用处理）
	saved := downloadTokenSecret
	downloadTokenSecret = nil
	defer func() { downloadTokenSecret = saved }()

	assert.Empty(t, GenerateDownloadToken("AbCd1234"))
	assert.False(t, VerifyDownloadToken("AbCd1234", "1.aa"))
}

func TestValidateEndpointURL(t *testing.T) {
	// scheme 白名单
	assert.ErrorIs(t, ValidateEndpointURL("ftp://minio.local:9000"), ErrEndpointURL)
	assert.ErrorIs(t, ValidateEndpointURL("file:///etc/passwd"), ErrEndpointURL)
	assert.ErrorIs(t, ValidateEndpointURL("http://"), ErrEndpointURL)
	// 公网 URL 通过（含 DNS 解析复判；example.com 解析结果为公网）
	assert.NoError(t, ValidateEndpointURL("https://example.com"))
	// 私网直写（默认拒绝；allow_private 未开）
	assert.ErrorIs(t, ValidateEndpointURL("http://192.168.1.10:9000"), ErrEndpointURL)
	assert.ErrorIs(t, ValidateEndpointURL("http://127.0.0.1:9000"), ErrEndpointURL)
	assert.ErrorIs(t, ValidateEndpointURL("http://10.0.0.5:5006"), ErrEndpointURL)
}
