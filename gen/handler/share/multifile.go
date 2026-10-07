// 多文件分享下载辅助（P0 多文件）：子文件单流 + zip 打包流。
// 鉴权与次数扣减已由 authorizeAndCharge 完成，这里只做读取与响应写出。
package share

import (
	"context"
	"fmt"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/pigeonbox/core/pkg/middleware"
	"github.com/pigeonbox/core/pkg/transfer"
	"github.com/pigeonbox/core/repo/db/model"
)

// streamChildFile 子文件单流下载（/share/download?file=<id>）。
// 写出响应返回 true；文件不存在/不属于该分享返回 false（调用方 404）。
func streamChildFile(ctx context.Context, c *app.RequestContext, code string, fileID uint, viewerIP string) bool {
	// 统一下传送流：本地后端 c.File 原生 Range；远端后端服务端区间流（206）
	// 或全量 200 回退（驱动不支持区间读时）。惰性开流：这里只做归属校验取
	// 子文件元数据，不预开全量读器（Range 请求会多一次无用打开）。
	fc, child, gerr := getShareService().GetShareChild(ctx, code, fileID)
	if gerr != nil || fc == nil || child == nil {
		return false
	}
	name := child.DisplayName()
	streamFileDownload(ctx, c, child.FilePath, name,
		func() { logDownload(ctx, fc, code, name, child.Size, viewerIP) })
	return true
}

// streamShareZip 多文件 zip 打包流式下载（/share/download 多文件分享的默认形态）。
// 已扣减 1 次取件（整包一次）；组装失败写出 500。
func streamShareZip(ctx context.Context, c *app.RequestContext, fileCode *model.FileCode, viewerIP string) bool {
	rc, err := getShareService().ZipStream(ctx, fileCode)
	if err != nil {
		return false
	}
	logDownload(ctx, fileCode, fileCode.Code, fileCode.Code+".zip", fileCode.Size, viewerIP)
	c.Header("Content-Type", "application/zip")
	c.Header("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.zip"`, fileCode.Code))
	c.Header("Cache-Control", "no-store")
	// 未知总长（-1）→ chunked 传输；读至 EOF 自动关闭
	c.SetBodyStream(newCloseOnEOFReader(rc), -1)
	return true
}

// logDownload 传输日志（下载，异步 fire-and-forget）
func logDownload(ctx context.Context, fc *model.FileCode, code, fileName string, size int64, viewerIP string) {
	var logUser *uint
	logUsername := ""
	if uid, ok := middleware.UserIDFromContext(ctx); ok {
		logUser = &uid
	}
	if n := middleware.UsernameFromContext(ctx); n != "" {
		logUsername = n
	}
	transfer.Record(transfer.Entry{
		Operation: transfer.OpDownload, FileCodeID: fc.ID, Code: code,
		FileName: fileName, FileSize: size,
		UserID: logUser, APIKeyID: apiKeyIDPtr(ctx),
		Username: logUsername, IP: viewerIP,
	})
}
