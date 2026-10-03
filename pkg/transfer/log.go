// Package transfer 提供传输日志（上传/下载）的异步记录。
//
// 背景：TransferLog 模型/DAO/前端页面早已存在，但全库无任何写入方，
// 传输日志页与趋势统计的下载序列一直是空数据——本包是写入侧的唯一收口。
// 记录失败只记运行日志，绝不影响主流程。
package transfer

import (
	"context"
	"time"

	"go.uber.org/zap"

	"github.com/filescodebox/core/pkg/logger"
	"github.com/filescodebox/core/repo/db/dao"
	"github.com/filescodebox/core/repo/db/model"
)

var repo = dao.NewTransferLogRepository()

// 操作类型常量（与 model.TransferLog.Operation 语义一致）
const (
	OpUpload   = "upload"
	OpDownload = "download"
)

// Entry 一次传输的归因信息。UserID/APIKeyID 为 nil 表示匿名/JWT 认证。
type Entry struct {
	Operation  string // upload | download
	FileCodeID uint
	Code       string
	FileName   string
	FileSize   int64
	UserID     *uint
	APIKeyID   *uint // 非 nil 表示该操作经用户级 API Key 认证（泄露排查归因用）
	Username   string
	IP         string
	DurationMs int64
}

// Record 异步记录一次传输。
func Record(e Entry) {
	go func() {
		defer func() { _ = recover() }()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		err := repo.Create(ctx, &model.TransferLog{
			Operation:  e.Operation,
			FileCodeID: e.FileCodeID,
			FileCode:   e.Code,
			FileName:   e.FileName,
			FileSize:   e.FileSize,
			UserID:     e.UserID,
			APIKeyID:   e.APIKeyID,
			Username:   e.Username,
			IP:         e.IP,
			DurationMs: e.DurationMs,
		})
		if err != nil {
			logger.Warn("transfer log write failed",
				zap.String("operation", e.Operation), zap.String("code", e.Code), zap.Error(err))
		}
	}()
}
