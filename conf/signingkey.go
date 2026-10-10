// signingkey.go 直传/分片会话签名密钥的单源取用。
//
// 此前 bootstrap wiring 与 chunk/token 各自 os.Getenv("PB_PRESIGN_SIGNING_KEY")
// + 回退 jwt_secret 现读现拼（两处逐字重复，配置来源漂移风险）；2026-10-10
// 收口于此。env 现读语义保留（与既有两处行为一致）。
package conf

import "os"

// PresignSigningKey 签名密钥：优先专用 env PB_PRESIGN_SIGNING_KEY，缺省回退
// 全局配置 jwt_secret；均无返回空串（消费方按"令牌通道关闭"降级——
// presign 直传令牌与 chunk 会话令牌共用本密钥）。
func PresignSigningKey() string {
	if k := os.Getenv("PB_PRESIGN_SIGNING_KEY"); k != "" {
		return k
	}
	if cfg := GetGlobalConfig(); cfg != nil {
		return cfg.User.JWTSecret
	}
	return ""
}
