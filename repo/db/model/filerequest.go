package model

import (
	"time"

	"gorm.io/gorm"
)

// FileRequest 寄件码/反向收件链接（P2：访客凭链接向指定用户投递文件）。
// 类似 Gokapi File Requests：用户创建一个投递链接（token），访客打开链接
// 上传文件；每个投递成为一个归属于链接主人的普通分享（复用分享的
// 过期/清理/通知机制），主人收到站内信+邮件通知。
type FileRequest struct {
	gorm.Model
	Token     string     `gorm:"uniqueIndex;size:64" json:"token"` // 链接令牌（crypto/rand 32 hex）
	Title     string     `gorm:"size:255" json:"title"`            // 投递页说明（访客可见）
	UserID    uint       `gorm:"index" json:"user_id"`             // 收件人（链接创建者）
	MaxFiles  int        `gorm:"default:0" json:"max_files"`       // 单次投递文件数上限（0=不限）
	MaxBytes  int64      `gorm:"default:0" json:"max_bytes"`       // 单次投递总大小上限字节（0=不限）
	ExpiredAt *time.Time `json:"expired_at"`                       // 链接过期时间（nil=永久）
	UsedCount int        `gorm:"default:0" json:"used_count"`      // 累计投递次数
	RecvBytes int64      `gorm:"default:0" json:"recv_bytes"`      // 累计接收字节
}

// IsExpired 链接是否已过期
func (r *FileRequest) IsExpired() bool {
	return r.ExpiredAt != nil && r.ExpiredAt.Before(time.Now())
}
