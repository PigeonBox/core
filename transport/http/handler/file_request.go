// 寄件码/反向收件 HTTP 端点（P2，手写路由）。
package handler

import (
	"context"
	"fmt"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	requestApp "github.com/filescodebox/core/app/request"
	"github.com/filescodebox/core/pkg/middleware"
	"github.com/filescodebox/core/pkg/resp"
	"github.com/filescodebox/core/pkg/transfer"
)

var requestSvc *requestApp.Service

// SetRequestService 注入寄件码服务（bootstrap 调用）
func SetRequestService(s *requestApp.Service) { requestSvc = s }

func getRequestService() *requestApp.Service { return requestSvc }

// ==================== 链接管理（登录用户） ====================

// UserCreateFileRequest 创建投递链接
func UserCreateFileRequest(ctx context.Context, c *app.RequestContext) {
	svc := getRequestService()
	if svc == nil {
		c.JSON(consts.StatusInternalServerError, map[string]interface{}{"code": 500, "message": "request service 未就绪"})
		return
	}
	uid, ok := userIDFromCtx(c)
	if !ok {
		resp.NewErrorWithMessage(c, 401, "请先登录")
		return
	}
	var body struct {
		Title       string `json:"title"`
		MaxFiles    int    `json:"max_files"`
		MaxBytes    int64  `json:"max_bytes"`
		ExpireValue int    `json:"expire_value"`
		ExpireStyle string `json:"expire_style"`
	}
	if err := c.BindJSON(&body); err != nil {
		c.JSON(consts.StatusBadRequest, map[string]interface{}{"code": 400, "message": "请求体解析失败"})
		return
	}
	fr, err := svc.Create(ctx, uid, requestApp.CreateReq{
		Title:       body.Title,
		MaxFiles:    body.MaxFiles,
		MaxBytes:    body.MaxBytes,
		ExpireValue: body.ExpireValue,
		ExpireStyle: body.ExpireStyle,
	})
	if err != nil {
		c.JSON(consts.StatusBadRequest, map[string]interface{}{"code": 400, "message": err.Error()})
		return
	}
	resp.Success(c, fr)
}

// UserListFileRequests 我的投递链接列表
func UserListFileRequests(ctx context.Context, c *app.RequestContext) {
	svc := getRequestService()
	if svc == nil {
		c.JSON(consts.StatusInternalServerError, map[string]interface{}{"code": 500, "message": "request service 未就绪"})
		return
	}
	uid, ok := userIDFromCtx(c)
	if !ok {
		resp.NewErrorWithMessage(c, 401, "请先登录")
		return
	}
	list, err := svc.ListForUser(ctx, uid)
	if err != nil {
		c.JSON(consts.StatusInternalServerError, map[string]interface{}{"code": 500, "message": err.Error()})
		return
	}
	resp.Success(c, list)
}

// UserDeleteFileRequest 撤销投递链接
func UserDeleteFileRequest(ctx context.Context, c *app.RequestContext) {
	svc := getRequestService()
	if svc == nil {
		c.JSON(consts.StatusInternalServerError, map[string]interface{}{"code": 500, "message": "request service 未就绪"})
		return
	}
	uid, ok := userIDFromCtx(c)
	if !ok {
		resp.NewErrorWithMessage(c, 401, "请先登录")
		return
	}
	token := c.Param("token")
	deleted, err := svc.Delete(ctx, uid, token)
	if err != nil {
		c.JSON(consts.StatusInternalServerError, map[string]interface{}{"code": 500, "message": err.Error()})
		return
	}
	if !deleted {
		c.JSON(consts.StatusNotFound, map[string]interface{}{"code": 404, "message": "链接不存在"})
		return
	}
	resp.Success(c, map[string]interface{}{"deleted": true})
}

// ==================== 访客侧 ====================

// GetFileRequestPublic 访客取链接信息（GET /request/:token）
func GetFileRequestPublic(ctx context.Context, c *app.RequestContext) {
	svc := getRequestService()
	if svc == nil {
		c.JSON(consts.StatusInternalServerError, map[string]interface{}{"code": 500, "message": "request service 未就绪"})
		return
	}
	view, err := svc.GetPublic(ctx, c.Param("token"))
	if err != nil {
		c.JSON(consts.StatusNotFound, map[string]interface{}{"code": 404, "message": err.Error()})
		return
	}
	resp.Success(c, view)
}

// GuestSubmitFiles 访客投递（POST /api/v1/request/:token/upload，multipart files[]）。
// 显式受邀上传：不走 upload.open_upload 总闸（与 Gokapi File Requests 同策略）。
func GuestSubmitFiles(ctx context.Context, c *app.RequestContext) {
	svc := getRequestService()
	if svc == nil || shareSvc == nil {
		c.JSON(consts.StatusInternalServerError, map[string]interface{}{"code": 500, "message": "service 未就绪"})
		return
	}
	token := c.Param("token")

	form, err := c.MultipartForm()
	if err != nil {
		c.JSON(consts.StatusBadRequest, map[string]interface{}{"code": 400, "message": "解析 multipart 失败: " + err.Error()})
		return
	}
	files := form.File["files"]
	if len(files) == 0 {
		files = form.File["file"]
	}
	if len(files) == 0 || len(files) > 100 {
		c.JSON(consts.StatusBadRequest, map[string]interface{}{"code": 400, "message": "files 需为 1-100 项"})
		return
	}

	// 预检链接（约束在 Submit 内还会强校验；此处 fail-fast 避免无效落盘）
	if _, err := svc.GetPublic(ctx, token); err != nil {
		c.JSON(consts.StatusNotFound, map[string]interface{}{"code": 404, "message": err.Error()})
		return
	}

	guestIP := middleware.ClientIP(c)
	st := manageStorage
	// 访客投递为明文上传，不跳过魔数校验（encrypted=false）
	stored, ok := saveUploadEntries(ctx, c, st, files, false)
	if !ok {
		return
	}

	shareResult, err := svc.Submit(ctx, token, stored, guestIP)
	if err != nil {
		cleanupStored(ctx, st, stored)
		c.JSON(consts.StatusBadRequest, map[string]interface{}{"code": 400, "message": err.Error()})
		return
	}

	transfer.Record(transfer.Entry{
		Operation: transfer.OpUpload, FileCodeID: shareResult.ID, Code: shareResult.Code,
		FileName: fmt.Sprintf("%d files (request)", len(stored)), FileSize: shareResult.Size,
		Username: middleware.UsernameFromContext(ctx), IP: guestIP,
	})

	c.JSON(consts.StatusOK, map[string]interface{}{
		"code":    200,
		"message": "投递成功",
		"data":    map[string]interface{}{"file_count": len(stored)},
	})
}
