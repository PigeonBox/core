package model

import (
	"path/filepath"
	"strings"
	"time"

	"gorm.io/gorm"
)

// 分享管控状态（治理 2026-10-03）。与软删除正交：
//   - normal        正常（默认；老库 AutoMigrate 补列后全部回填 normal）
//   - blocked       管理员禁用（不可取件/下载，可恢复，记录留证）
//   - pending_review 内容待审核（moderation 命中且策略为 pending 时置入，审核通过恢复 normal）
const (
	StatusNormal        = "normal"
	StatusBlocked       = "blocked"
	StatusPendingReview = "pending_review"
)

// ValidShareStatus 状态机合法取值白名单（写入口统一校验）。
func ValidShareStatus(s string) bool {
	switch s {
	case StatusNormal, StatusBlocked, StatusPendingReview:
		return true
	}
	return false
}

// FileCode 文件代码模型
type FileCode struct {
	gorm.Model
	Code         string     `gorm:"uniqueIndex;size:255" json:"code"`
	Prefix       string     `gorm:"size:255" json:"prefix"`
	Suffix       string     `gorm:"size:255" json:"suffix"`
	UUIDFileName string     `gorm:"size:255" json:"uuid_file_name"`
	FilePath     string     `gorm:"size:255" json:"file_path"`
	Size         int64      `gorm:"default:0" json:"size"`
	Text         string     `gorm:"type:text" json:"text"`
	ExpiredAt    *time.Time `gorm:"index:idx_file_codes_status_expired,priority:2" json:"expired_at"`
	ExpiredCount int        `gorm:"default:0" json:"expired_count"` // 剩余可取次数：-1=无限, 0=已耗尽, >0=剩余
	UsedCount    int        `gorm:"default:0" json:"used_count"`
	// Status 管控状态，默认 normal；blocked/pending_review 拒绝取件（索引覆盖管理端按状态筛选）
	Status string `gorm:"size:20;default:'normal';index:idx_file_codes_status_expired,priority:1" json:"status"`

	FileHash  string `gorm:"size:64" json:"file_hash"`
	IsChunked bool   `gorm:"default:false" json:"is_chunked"`
	UploadID  string `gorm:"size:36" json:"upload_id"`

	// 新增：用户认证相关字段
	UserID      *uint  `gorm:"index" json:"user_id"`                           // 上传用户ID，为null表示匿名上传
	UploadType  string `gorm:"size:20;default:'anonymous'" json:"upload_type"` // anonymous, authenticated
	RequireAuth bool   `gorm:"default:false" json:"require_auth"`              // 是否需要密码才能下载
	PasswordHash string `gorm:"size:255" json:"-"`                              // 取件密码的 bcrypt 哈希（json:"-" 不外泄）
	OwnerIP     string `gorm:"size:45" json:"owner_ip"`                        // 上传者IP地址
	// Encrypted 端到端客户端加密（P1 E2E）：true 时存储的是密文，密钥经分享链接
	// #fragment 传递、从不落服务端；服务端零知识（预览/服务端 zip 打包不适用）
	Encrypted bool `gorm:"default:false" json:"encrypted"`

	// 取件追踪（软删除字段 gorm.Model.DeletedAt 已自带）
	ViewerIP       string     `gorm:"size:45" json:"viewer_ip"`      // 最近一次取件人IP
	ViewerAt       *time.Time `json:"viewer_at"`                     // 最近一次取件时间
	ViewerCount    int        `gorm:"default:0" json:"viewer_count"` // 累计取件次数
	LastNotifiedAt *time.Time `json:"last_notified_at"`              // 最近一次给 owner 发通知的时间（用于去重）
}

// IsExpired 检查是否过期
func (f *FileCode) IsExpired() bool {
	// 检查时间过期
	if f.ExpiredAt != nil && f.ExpiredAt.Before(time.Now()) {
		return true
	}

	// 检查次数过期
	// ExpiredCount 语义：-1=无限(不过期), 0=已耗尽(过期), >0=剩余(不过期)
	if f.ExpiredCount == 0 {
		return true
	}

	return false
}

// GetFilePath 获取文件路径
func (f *FileCode) GetFilePath() string {
	// 新格式：FilePath（目录）+ UUIDFileName（文件名）
	if f.FilePath != "" && f.UUIDFileName != "" {
		// 检查FilePath是否已经包含了文件名（兼容性处理）
		// 如果FilePath已经是完整路径（包含文件扩展名），直接返回
		if strings.Contains(f.FilePath, f.UUIDFileName) {
			return f.FilePath // 旧格式：FilePath包含完整路径
		}
		// 新格式：组合目录和文件名
		return filepath.Join(f.FilePath, f.UUIDFileName)
	}

	// 兼容旧格式：file_path 字段直接包含完整的相对路径
	if f.FilePath != "" {
		return f.FilePath
	}

	return ""
}

// IsBlockedShare 分享是否处于管控拒绝态（blocked / pending_review）
func (f *FileCode) IsBlockedShare() bool {
	return f.Status == StatusBlocked || f.Status == StatusPendingReview
}

// IsTextShare 判定是否纯文本分享：Text 非空且无文件路径。
//
// 回归要点（P0）：文件分享会把原始文件名存进 Text 字段，因此仅凭
// Text != "" 判定会把文件分享误判为文本——文件下载曾被文本分支拦截，
// 返回文件名字符串而非文件内容。判定逻辑此前在 app/share 包级函数，
// 多域（anonymous/mcp/presign）需要时各自 import share——下沉到 model
// 作为领域事实，消费方零跨域依赖。
func (f *FileCode) IsTextShare() bool {
	return f != nil && f.Text != "" && f.FilePath == ""
}

// FileCodeQuery 管理端文件列表过滤条件（DAO ListWithFilter 消费）。
// 全字段可选，零值 = 不过滤。
type FileCodeQuery struct {
	Keyword       string     // 模糊匹配 code/prefix/suffix/uuid_file_name/text
	UserID        *uint      // 上传者
	UploadType    string     // anonymous/authenticated/presign_*
	OwnerIP       string     // 上传者 IP（滥用定位）
	Status        string     // normal/blocked/pending_review
	MinSize       *int64     // 大小区间（字节）
	MaxSize       *int64
	CreatedAfter  *time.Time // 创建时间区间
	CreatedBefore *time.Time
	Expired       *bool // true=仅过期(时间或次数)，false=仅未过期
	Page          int
	PageSize      int
}

// FileCodeUpdate 文件代码更新数据
type FileCodeUpdate struct {
	gorm.Model
	ExpiredAt    *time.Time `json:"expired_at"`
	ExpiredCount *int       `json:"expired_count"`
	UsedCount    *int       `json:"used_count"`
	RequireAuth  *bool      `json:"require_auth"`
	OwnerIP      *string    `json:"owner_ip"`
}

// FileCodeStats 文件统计查询结果
type FileCodeStats struct {
	gorm.Model
	TotalFiles     int64 `json:"total_files"`
	TotalSize      int64 `json:"total_size"`
	TodayUploads   int64 `json:"today_uploads"`
	TodayDownloads int64 `json:"today_downloads"`
	ExpiredFiles   int64 `json:"expired_files"`
	AnonymousFiles int64 `json:"anonymous_files"`
	UserFiles      int64 `json:"user_files"`
}
