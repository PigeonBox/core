// NAS 本地文件免上传导入端点（P3，手写路由）。
package handler

import (
	"context"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	shareService "github.com/filescodebox/core/app/share"
	"github.com/filescodebox/core/pkg/middleware"
	"github.com/filescodebox/core/pkg/resp"
	"github.com/filescodebox/core/pkg/transfer"
	"github.com/filescodebox/core/pkg/utils"
)

// UserImportLocal 本地文件导入为分享（POST /api/v1/user/shares/import-local）。
// 请求体：{path, expire_value, expire_style, require_auth, password, custom_code}
// 仅登录用户可用；路径必须落在 upload.local_import.roots 白名单内。
func UserImportLocal(ctx context.Context, c *app.RequestContext) {
	if shareSvc == nil {
		c.JSON(consts.StatusInternalServerError, map[string]interface{}{"code": 500, "message": "share service 未就绪"})
		return
	}
	uid, ok := userIDFromCtx(c)
	if !ok {
		resp.NewErrorWithMessage(c, 401, "请先登录")
		return
	}
	var body struct {
		Path        string `json:"path"`
		ExpireValue int    `json:"expire_value"`
		ExpireStyle string `json:"expire_style"`
		RequireAuth bool   `json:"require_auth"`
		Password    string `json:"password"`
		CustomCode  string `json:"custom_code"`
	}
	if err := c.BindJSON(&body); err != nil || body.Path == "" {
		c.JSON(consts.StatusBadRequest, map[string]interface{}{"code": 400, "message": "请求体无效（需 path）"})
		return
	}
	if body.ExpireStyle == "" {
		body.ExpireStyle = "day"
		if body.ExpireValue <= 0 {
			body.ExpireValue = 7
		}
	}
	if err := utils.CheckExpireStyleAllowed(body.ExpireStyle); err != nil {
		c.JSON(consts.StatusBadRequest, map[string]interface{}{"code": 400, "message": err.Error()})
		return
	}
	passwordHash := ""
	if body.RequireAuth {
		if body.Password == "" {
			c.JSON(consts.StatusBadRequest, map[string]interface{}{"code": 400, "message": "开启密码保护时必须提供密码"})
			return
		}
		hash, err := utils.HashPassword(body.Password)
		if err != nil {
			c.JSON(consts.StatusInternalServerError, map[string]interface{}{"code": 500, "message": "密码处理失败"})
			return
		}
		passwordHash = hash
	}

	result, err := shareSvc.ImportLocalFile(ctx, shareService.ImportLocalOpts{
		AbsPath:      body.Path,
		ExpireValue:  body.ExpireValue,
		ExpireStyle:  body.ExpireStyle,
		RequireAuth:  body.RequireAuth,
		PasswordHash: passwordHash,
		CustomCode:   body.CustomCode,
		UserID:       &uid,
		OwnerIP:      middleware.ClientIP(c),
	})
	if err != nil {
		c.JSON(consts.StatusBadRequest, map[string]interface{}{"code": 400, "message": err.Error()})
		return
	}

	transfer.Record(transfer.Entry{
		Operation: transfer.OpUpload, FileCodeID: result.ID, Code: result.Code,
		FileName: result.Text, FileSize: result.Size,
		UserID: &uid, APIKeyID: apiKeyIDPtr(ctx),
		Username: middleware.UsernameFromContext(ctx), IP: middleware.ClientIP(c),
	})

	c.JSON(consts.StatusOK, map[string]interface{}{
		"code":    200,
		"message": "导入成功",
		"data": map[string]interface{}{
			"code":      result.Code,
			"share_url": result.FullShareURL,
		},
	})
}
