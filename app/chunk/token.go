// token.go 分片会话令牌与归属校验的单一实现。
//
// 此前 transport/handler（multi-bind）与 gen/handler/chunk 各持一份逐字孪生
// （chunkSessionToken/verifyChunkToken/ownedByCaller，注释自认"同源同语义"），
// 收口到本域导出函数后两处只留薄委托。
package chunk

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"os"

	"github.com/filescodebox/core/conf"
	"github.com/filescodebox/core/repo/db/model"
)

// SessionToken 分片会话令牌：HMAC("chunk-session:"+uploadID, key)。客户端从
// init 响应头 X-Upload-Token 取得并在后续请求回传，即可在 IP 漂移（移动网络/
// CGNAT）后仍通过归属校验。密钥复用 presign 签名密钥（FCB_PRESIGN_SIGNING_KEY，
// 缺省回退 jwt_secret）；无密钥返回空串（令牌通道关闭，仅 IP/用户匹配生效）。
func SessionToken(uploadID string) string {
	key := os.Getenv("FCB_PRESIGN_SIGNING_KEY")
	if key == "" {
		if cfg := conf.GetGlobalConfig(); cfg != nil {
			key = cfg.User.JWTSecret
		}
	}
	if key == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(key))
	_, _ = mac.Write([]byte("chunk-session:" + uploadID))
	return hex.EncodeToString(mac.Sum(nil))
}

// VerifySessionToken 客户端回传的会话令牌是否有效。
func VerifySessionToken(uploadID, token string) bool {
	want := SessionToken(uploadID)
	return want != "" && token != "" && hmac.Equal([]byte(want), []byte(token))
}

// OwnedByCaller 分片会话归属校验（治理）：控制记录记有 OwnerIP 时，要求
// IP 一致、同一登录用户或持有效会话令牌；老数据（OwnerIP 为空）跳过保持兼容。
// clientIP/callerUserID 由调用方按各路由的身份来源提取后传入（JWT/匿名、
// ctx 值/c.Get 两种路径判定口径一致）。
func OwnedByCaller(info *model.UploadChunk, clientIP string, sessionToken string, callerUserID *uint) bool {
	if info.OwnerIP == "" {
		return true
	}
	if info.OwnerIP == clientIP {
		return true
	}
	if VerifySessionToken(info.UploadID, sessionToken) {
		return true
	}
	if info.UserID != nil && callerUserID != nil && *callerUserID == *info.UserID {
		return true
	}
	return false
}
