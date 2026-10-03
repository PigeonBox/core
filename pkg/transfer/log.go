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

// Record 异步记录一次传输。userID/username 为空表示匿名。
func Record(operation string, fileCodeID uint, code, fileName string, fileSize int64, userID *uint, username, ip string, durationMs int64) {
	go func() {
		defer func() { _ = recover() }()
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		err := repo.Create(ctx, &model.TransferLog{
			Operation:  operation,
			FileCodeID: fileCodeID,
			FileCode:   code,
			FileName:   fileName,
			FileSize:   fileSize,
			UserID:     userID,
			Username:   username,
			IP:         ip,
			DurationMs: durationMs,
		})
		if err != nil {
			logger.Warn("transfer log write failed",
				zap.String("operation", operation), zap.String("code", code), zap.Error(err))
		}
	}()
}
