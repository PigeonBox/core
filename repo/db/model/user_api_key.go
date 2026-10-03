package model

import (
	"time"

	"gorm.io/gorm"
)

// UserAPIKey 表示用户生成的 API Key 元信息，真实密钥仅在创建时返回
// KeyHash 存储经过 SHA-256 处理后的摘要，Prefix 便于调试时区分不同密钥
// Revoked 标记密钥是否已被撤销
// ExpiresAt 可选过期时间，为空表示长期有效
// LastUsedAt/LastUsedIP 记录最近一次使用时间与来源 IP（泄露排查）
// ExpiryNotifiedAt 临期通知发送时间（去重）
// Name 为用户自定义的标识，方便区分多把密钥

type UserAPIKey struct {
	gorm.Model
	UserID     uint   `gorm:"index"`
	Name       string `gorm:"size:100"`
	Prefix     string `gorm:"size:16"`
	KeyHash    string `gorm:"size:64;uniqueIndex"`
	LastUsedAt *time.Time
	LastUsedIP string `gorm:"size:45"`
	ExpiresAt  *time.Time
	// ExpiryNotifiedAt 临期通知去重标记；NULL=未通知
	ExpiryNotifiedAt *time.Time
	RevokedAt        *time.Time
	Revoked          bool `gorm:"default:false"`
}

// TableName 指定表名
func (UserAPIKey) TableName() string {
	return "user_api_keys"
}
