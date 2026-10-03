package preview

import (
	"context"
	"fmt"
	"io"
	"os"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/filescodebox/contracts/errcode"
	"github.com/filescodebox/core/pkg/gate"
	"github.com/filescodebox/core/pkg/middleware"
	"github.com/filescodebox/core/pkg/resp"
	"github.com/filescodebox/core/pkg/utils"
	previewService "github.com/filescodebox/core/preview"
	"github.com/filescodebox/core/repo/db/dao"
	dao_preview "github.com/filescodebox/core/repo/db/dao_preview"
	"github.com/filescodebox/core/repo/db/model"
	"github.com/filescodebox/core/storage"
)

// storageSvc 统一存储实例（bootstrap 注入；nil 时预览生成不可用）
var storageSvc storage.StorageInterface

// SetStorage 注入统一存储实例（bootstrap 调用）。
// 回归（2026-10-03）：此前硬编码 data/uploads 路径基，与统一存储不一致。
func SetStorage(st storage.StorageInterface) {
	storageSvc = st
}

// GetPreview 获取文件预览信息
// @router /preview/:code [GET]
func GetPreview(ctx context.Context, c *app.RequestContext) {
	code := c.Param("code")
	if code == "" {
		c.JSON(consts.StatusBadRequest, map[string]interface{}{
			"code":    400,
			"message": "请提供分享码",
		})
		return
	}

	// 下载闸门 + 分享管控（回归 P0：此前裸查 DB，blocked/待审/过期/密码/登录闸门
	// 全部旁路，违规内容可被匿名预览提取全文）
	var previewUserID *uint
	if v, ok := middleware.UserIDFromContext(ctx); ok {
		previewUserID = &v
	}
	if err := gate.CheckDownloadLogin(previewUserID); err != nil {
		resp.NewTypedError(c, err)
		return
	}

	// 获取文件信息
	fileCodeRepo := dao.NewFileCodeRepository()
	fileCode, err := fileCodeRepo.GetByCode(ctx, code)
	if err != nil {
		c.JSON(consts.StatusNotFound, map[string]interface{}{
			"code":    404,
			"message": "分享不存在",
		})
		return
	}
	if fileCode.IsExpired() {
		c.JSON(consts.StatusNotFound, map[string]interface{}{
			"code":    404,
			"message": "分享不存在或已过期",
		})
		return
	}
	if fileCode.IsBlockedShare() {
		resp.NewErrorByCode(c, errcode.CodeShareBlocked)
		return
	}

	// 密码保护分享：预览必须携带正确密码（防爆破锁定与取件查询同规格）
	if fileCode.RequireAuth {
		lock := middleware.GetDefaultLockout()
		lockKey := middleware.FormatLockKey("preview", middleware.ClientIP(c), code)
		if remain, locked := lock.CheckLocked(ctx, lockKey); locked {
			c.JSON(consts.StatusTooManyRequests, map[string]interface{}{
				"code":    errcode.CodeTooManyAttempts,
				"message": fmt.Sprintf("尝试过于频繁，已临时锁定，请 %d 秒后重试", remain),
			})
			return
		}
		password := c.Query("password")
		if password == "" || fileCode.PasswordHash == "" || !utils.CheckPassword(fileCode.PasswordHash, password) {
			_, _ = lock.RecordFailure(ctx, lockKey)
			c.JSON(consts.StatusUnauthorized, map[string]interface{}{
				"code":    errcode.CodeSharePasswordWrong,
				"message": "需要密码",
				"data":    map[string]interface{}{"has_password": true},
			})
			return
		}
		lock.Reset(ctx, lockKey)
	}

	// 获取预览信息
	previewRepo := dao_preview.NewFilePreviewRepository()
	preview, err := previewRepo.GetByFileCodeID(ctx, fileCode.ID)
	if err != nil {
		// 预览不存在，尝试生成
		preview, err = generatePreview(ctx, fileCode)
		if err != nil {
			c.JSON(consts.StatusNotFound, map[string]interface{}{
				"code":    404,
				"message": "预览不可用",
			})
			return
		}
	}

	c.JSON(consts.StatusOK, map[string]interface{}{
		"code":    200,
		"message": "获取成功",
		"data":    preview,
	})
}

// generatePreview 生成预览。经统一存储实例读取文件（回归：此前硬编码
// data/uploads 路径基且未拼 UUIDFileName，与存储布局不符）。
func generatePreview(ctx context.Context, fileCode *model.FileCode) (*model.FilePreview, error) {
	if storageSvc == nil {
		return nil, fmt.Errorf("storage not available")
	}
	filePath := fileCode.GetFilePath()
	if filePath == "" {
		return nil, fmt.Errorf("file path is empty")
	}
	rc, _, err := storageSvc.GetFileReader(ctx, filePath)
	if err != nil {
		return nil, fmt.Errorf("open file from storage: %w", err)
	}
	// GeneratePreview 以磁盘路径为输入，落临时文件后清理
	tmp, err := os.CreateTemp("", "fcb-preview-*")
	if err != nil {
		_ = rc.Close()
		return nil, fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() { _ = os.Remove(tmpPath) }()
	_, copyErr := io.Copy(tmp, rc)
	_ = rc.Close()
	_ = tmp.Close()
	if copyErr != nil {
		return nil, fmt.Errorf("stage file: %w", copyErr)
	}
	return generatePreviewFromPath(ctx, fileCode, tmpPath)
}

func generatePreviewFromPath(ctx context.Context, fileCode *model.FileCode, filePath string) (*model.FilePreview, error) {
	// 判断文件类型
	ext := fileCode.Suffix
	if ext == "" {
		// 从UUID文件名提取扩展名
		if fileCode.UUIDFileName != "" {
			for i := len(fileCode.UUIDFileName) - 1; i >= 0; i-- {
				if fileCode.UUIDFileName[i] == '.' {
					ext = fileCode.UUIDFileName[i:]
					break
				}
			}
		}
		// 如果还是没有，从Text字段（原始文件名）提取
		if ext == "" && fileCode.Text != "" {
			for i := len(fileCode.Text) - 1; i >= 0; i-- {
				if fileCode.Text[i] == '.' {
					ext = fileCode.Text[i:]
					break
				}
			}
		}
	}

	// 获取预览服务
	svc := previewService.GetService()
	if svc == nil {
		return nil, fmt.Errorf("preview service not available")
	}

	// 生成预览（filePath 由调用方经统一存储暂存提供）
	previewData, err := svc.GeneratePreview(ctx, filePath, ext)
	if err != nil {
		return nil, fmt.Errorf("failed to generate preview: %w", err)
	}

	// 保存预览信息到数据库
	preview := &model.FilePreview{
		FileCodeID:  fileCode.ID,
		PreviewType: string(previewData.Type),
		Thumbnail:   previewData.Thumbnail,
		PreviewURL:  previewData.PreviewURL,
		Width:       previewData.Width,
		Height:      previewData.Height,
		Duration:    previewData.Duration,
		PageCount:   previewData.PageCount,
		TextContent: previewData.TextContent,
		MimeType:    previewData.MimeType,
		FileSize:    previewData.FileSize,
	}

	previewRepo := dao_preview.NewFilePreviewRepository()
	if err := previewRepo.Create(ctx, preview); err != nil {
		return nil, fmt.Errorf("failed to save preview: %w", err)
	}

	return preview, nil
}
