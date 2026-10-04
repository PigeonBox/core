// Package preview 预览域服务：取件校验与预览生成/持久化的业务编排。
//
// gen/handler/preview 是 HTTP 适配层（参数提取 + 错误→状态码映射），
// 闸门/过期/封禁/防爆破/密码/存储读取/预览生成/落库的业务事实全部收口于此。
package preview

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"github.com/filescodebox/core/pkg/gate"
	"github.com/filescodebox/core/pkg/middleware"
	"github.com/filescodebox/core/pkg/utils"
	previewService "github.com/filescodebox/core/preview"
	"github.com/filescodebox/core/repo/db/dao"
	dao_preview "github.com/filescodebox/core/repo/db/dao_preview"
	"github.com/filescodebox/core/repo/db/model"
	"github.com/filescodebox/core/storage"
)

// 业务错误（HTTP 适配层据此映射状态码与文案，不泄露内部细节）。
var (
	// ErrShareNotFound 分享不存在/已过期/不可见（统一 404 语义）
	ErrShareNotFound = errors.New("share not found or expired")
	// ErrShareBlocked 分享被封禁或待审（走 errcode 管控语义）
	ErrShareBlocked = errors.New("share blocked")
	// ErrPasswordRequired 密码保护分享未通过密码校验（含防爆破锁定态）
	ErrPasswordRequired = errors.New("password required")
	// ErrLocked 密码错误次数过多，临时锁定
	ErrLocked = errors.New("too many attempts, temporarily locked")
	// ErrPreviewUnavailable 预览不可用（生成失败/存储不可达）
	ErrPreviewUnavailable = errors.New("preview unavailable")
)

// LockedError 防爆破锁定态（携带剩余锁定秒数，供文案）
type LockedError struct{ Remain int }

func (e *LockedError) Error() string { return fmt.Sprintf("locked for %ds", e.Remain) }

// Service 预览域服务。存储实例由 bootstrap 注入；nil 时预览生成不可用
// （回归 2026-10-03：此前硬编码 data/uploads 路径基，与统一存储不一致）。
type Service struct {
	storageSvc storage.StorageInterface
}

// NewService 构造预览域服务。
func NewService(st storage.StorageInterface) *Service {
	return &Service{storageSvc: st}
}

// GetReq 预览获取请求。HTTP 关注点（取件码/密码/客户端 IP/登录态）由
// 适配层提取后传入；锁定、校验、生成、落库均为域内事实。
type GetReq struct {
	Code     string
	UserID   *uint  // 登录态（nil = 匿名）
	ClientIP string // 可信代理解析后的取件方 IP（防爆破锁定键）
	Password string // 密码保护分享的取件密码
}

// GetOrCreate 取预览：无则生成并持久化（get-or-create）。
func (s *Service) GetOrCreate(ctx context.Context, req GetReq) (*model.FilePreview, error) {
	// 下载闸门 + 分享管控（回归 P0：此前裸查 DB，blocked/待审/过期/密码/登录闸门
	// 全部旁路，违规内容可被匿名预览提取全文）
	if err := gate.CheckDownloadLogin(req.UserID); err != nil {
		return nil, err
	}

	// 获取文件信息
	fileCodeRepo := dao.NewFileCodeRepository()
	fileCode, err := fileCodeRepo.GetByCode(ctx, req.Code)
	if err != nil {
		return nil, ErrShareNotFound
	}
	if fileCode.IsExpired() {
		return nil, ErrShareNotFound
	}
	if fileCode.IsBlockedShare() {
		return nil, ErrShareBlocked
	}

	// 密码保护分享：预览必须携带正确密码（防爆破锁定与取件查询同规格）
	if fileCode.RequireAuth {
		lock := middleware.GetDefaultLockout()
		lockKey := middleware.FormatLockKey("preview", req.ClientIP, req.Code)
		if remain, locked := lock.CheckLocked(ctx, lockKey); locked {
			return nil, &LockedError{Remain: remain}
		}
		if req.Password == "" || fileCode.PasswordHash == "" || !utils.CheckPassword(fileCode.PasswordHash, req.Password) {
			_, _ = lock.RecordFailure(ctx, lockKey)
			return nil, ErrPasswordRequired
		}
		lock.Reset(ctx, lockKey)
	}

	// 取预览；不存在则生成
	previewRepo := dao_preview.NewFilePreviewRepository()
	preview, err := previewRepo.GetByFileCodeID(ctx, fileCode.ID)
	if err == nil {
		return preview, nil
	}
	preview, err = s.generatePreview(ctx, fileCode)
	if err != nil {
		return nil, ErrPreviewUnavailable
	}
	return preview, nil
}

// generatePreview 生成预览。经统一存储实例读取文件（回归：此前硬编码
// data/uploads 路径基且未拼 UUIDFileName，与存储布局不符）。
func (s *Service) generatePreview(ctx context.Context, fileCode *model.FileCode) (*model.FilePreview, error) {
	if s.storageSvc == nil {
		return nil, fmt.Errorf("storage not available")
	}
	filePath := fileCode.GetFilePath()
	if filePath == "" {
		return nil, fmt.Errorf("file path is empty")
	}
	rc, _, err := s.storageSvc.GetFileReader(ctx, filePath)
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
	return s.generatePreviewFromPath(ctx, fileCode, tmpPath)
}

func (s *Service) generatePreviewFromPath(ctx context.Context, fileCode *model.FileCode, filePath string) (*model.FilePreview, error) {
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

	// 获取预览引擎
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
