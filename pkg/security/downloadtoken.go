// Package security 提供独立的安全原语：
//   - DownloadToken: 取件下载令牌（时间窗 HMAC + 恒时比较）
//   - SSRF: 存储端点 URL 校验（scheme 白名单 + 私网地址策略）
package security

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// downloadTokenTTL 默认令牌有效期（对齐上游 ~1000s 窗口）
const downloadTokenTTL = 1000 * time.Second

// ErrDownloadToken 无效或过期的下载令牌
var ErrDownloadToken = errors.New("下载令牌无效或已过期，请重新获取取件信息")

// downloadTokenSecret 令牌签名密钥（bootstrap 注入，默认复用 jwt_secret）
var downloadTokenSecret []byte

// SetDownloadTokenSecret 注入下载令牌签名密钥（应与 jwt_secret 等强的独立随机值）。
func SetDownloadTokenSecret(secret string) {
	if secret == "" {
		return
	}
	downloadTokenSecret = []byte(secret)
}

// tokenPayload 令牌消息 = code + "." + 过期 Unix 秒
func tokenPayload(code string, exp int64) string {
	return code + "." + strconv.FormatInt(exp, 10)
}

func sign(payload string) string {
	mac := hmac.New(sha256.New, downloadTokenSecret)
	mac.Write([]byte(payload))
	return hex.EncodeToString(mac.Sum(nil))
}

// GenerateDownloadToken 生成下载令牌 "exp.sig"。
// 空密钥（未注入）时返回空串——调用方按"功能未启用"处理。
func GenerateDownloadToken(code string) string {
	if len(downloadTokenSecret) == 0 {
		return ""
	}
	exp := time.Now().Add(downloadTokenTTL).Unix()
	return fmt.Sprintf("%d.%s", exp, sign(tokenPayload(code, exp)))
}

// VerifyDownloadToken 恒时比较校验令牌（exp 窗口 + HMAC 签名）。
func VerifyDownloadToken(code, token string) bool {
	if len(downloadTokenSecret) == 0 {
		return false
	}
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return false
	}
	exp, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return false
	}
	expected := sign(tokenPayload(code, exp))
	return hmac.Equal([]byte(expected), []byte(parts[1]))
}
