package share

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/filescodebox/core/app/moderation"
	"github.com/filescodebox/core/pkg/logger"
	"github.com/filescodebox/core/pkg/metrics"
	"github.com/filescodebox/core/pkg/utils"
	"github.com/filescodebox/core/repo/db/dao"
	"github.com/filescodebox/core/repo/db/model"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

// StoredFileEntry 已落存储的文件项（多文件分享创建入参）。
// 由传输层负责"物理文件已就位"（direct 保存 / chunk 合并 / presign HeadObject 核实），
// 本层只做业务装配：配额、审核、主表+子表写入。
type StoredFileEntry struct {
	// RelPath 存储相对路径（含唯一文件名；local 下与合并写入路径一致）
	RelPath string
	// FileName 原始文件名（消毒后，仅展示）
	FileName string
	// Size 实际大小（handler 已复核）
	Size int64
	// FileHash SHA-256（可空：哈希失败不阻断分享）
	FileHash string
}

// MultiShareReq 多文件分享创建请求。
type MultiShareReq struct {
	Entries      []StoredFileEntry
	ExpiredAt    *time.Time
	ExpiredCount int
	RequireAuth  bool
	PasswordHash string
	UserID       *uint
	UploadType   string
	OwnerIP      string
	Channel      string // direct/chunk/presign（metrics/审核 meta 用）
	Encrypted    bool   // E2E：所有子文件均为客户端密文（P1）
	CustomCode   string // 自定义取件码（P3；登录用户专属，handler 把关）
}

// ShareFileItem 分享内单文件视图（取件端/管理端通用）。
type ShareFileItem struct {
	ID       uint   `json:"id"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	FileHash string `json:"file_hash"`
}

// maxMultiFiles 单个分享的文件数上限（防滥用；upload.max_files_per_share 可调）
const maxMultiFiles = 100

// fileCodeFileRepo 惰性子文件 DAO（share service 既有字段模式）
func (s *Service) fileRepo() *dao.FileCodeFileRepository {
	if s.fileFileRepo == nil {
		s.fileFileRepo = dao.NewFileCodeFileRepository()
	}
	return s.fileFileRepo
}

// CreateMultiFileShare 创建多文件分享（1 个取件码 ↔ N 个文件）。
//
// 与单文件 CreateShare 的差异：
//   - 主表 Size = 各文件之和；legacy 字段（FilePath/UUIDFileName/FileHash）与首个文件同步，
//     保证旧读取方（管理端列表/秒传/统计）零改动可用
//   - 子表每文件一行，取件端经 ListShareFiles 拉取
//   - 配额按总大小一次扣检；审核逐文件判定（任一 reject 整单拒绝，pending 整单待审）
//
// 失败时不回滚物理文件——调用方（handler）负责在 err != nil 时清理已落盘文件。
func (s *Service) CreateMultiFileShare(ctx context.Context, req *MultiShareReq) (*ShareResp, error) {
	s.ensureRepository()
	if len(req.Entries) == 0 {
		return nil, errors.New("至少需要一个文件")
	}
	if len(req.Entries) > maxMultiFiles {
		return nil, fmt.Errorf("单次分享文件数超过上限（%d 个）", maxMultiFiles)
	}

	var totalSize int64
	for _, e := range req.Entries {
		if e.RelPath == "" {
			return nil, errors.New("文件路径为空")
		}
		totalSize += e.Size
	}

	// 单用户单次上传总大小上限（与单文件语义一致，按合计校验）
	if req.UserID != nil && s.userService != nil {
		if capSize := s.userService.GetUploadSizeCap(ctx, *req.UserID); capSize > 0 {
			if err := utils.CheckUploadSize(totalSize, capSize); err != nil {
				return nil, fmt.Errorf("上传总大小超过限制（上限 %d 字节）", capSize)
			}
		}
	}

	// 存储配额（合计一次扣检）
	if err := s.checkQuota(ctx, req.UserID, totalSize); err != nil {
		metrics.RecordRejected(metrics.RejectQuota)
		return nil, err
	}

	// 内容审核：逐文件判定，任一命中即整单处置（reject 拒绝 / pending 待审）
	filePending := false
	if s.moderator != nil {
		for _, e := range req.Entries {
			meta := moderation.UploadMeta{
				FileName:    e.FileName,
				Size:        e.Size,
				OwnerIP:     req.OwnerIP,
				UserID:      req.UserID,
				Channel:     req.Channel,
				StoragePath: e.RelPath, // 文件内容扫描（ClamAV）读取路径
			}
			switch s.moderator.InspectFile(ctx, meta) {
			case moderation.VerdictReject:
				metrics.RecordModerationHit("reject")
				metrics.RecordRejected(metrics.RejectModerated)
				return nil, &ContentRejectedError{}
			case moderation.VerdictPending:
				metrics.RecordModerationHit("pending")
				filePending = true
			}
		}
	}

	first := req.Entries[0]
	fileCode, err := s.createWithCode(ctx, req.CustomCode, func(code string) *model.FileCode {
		return &model.FileCode{
			Code:         code,
			FilePath:     first.RelPath,
			UUIDFileName: uniqueNameOf(first.RelPath),
			Size:         totalSize,
			Text:         first.FileName, // 文件分享的 Text 存首个原始文件名（与单文件直传通道一致）
			ExpiredAt:    req.ExpiredAt,
			ExpiredCount: req.ExpiredCount,
			RequireAuth:  req.RequireAuth,
			PasswordHash: req.PasswordHash,
			UserID:       req.UserID,
			UploadType:   req.UploadType,
			OwnerIP:      req.OwnerIP,
			FileHash:     first.FileHash,
			Encrypted:    req.Encrypted,
		}
	})
	if err != nil {
		return nil, err
	}

	// 子表批量写入；失败则回滚主表（分享未发布，物理文件交 handler 清理）
	children := make([]*model.FileCodeFile, 0, len(req.Entries))
	for i, e := range req.Entries {
		children = append(children, &model.FileCodeFile{
			FileCodeID:   fileCode.ID,
			FileName:     e.FileName,
			UUIDFileName: uniqueNameOf(e.RelPath),
			FilePath:     e.RelPath,
			Size:         e.Size,
			FileHash:     e.FileHash,
			SortOrder:    i,
		})
	}
	if err := s.fileRepo().CreateBatch(ctx, children); err != nil {
		_ = s.fileCodeRepo.Delete(ctx, fileCode.ID)
		return nil, fmt.Errorf("写入分享文件列表失败: %w", err)
	}

	// 用户统计：次数 +1，存储按合计
	if s.userService != nil && req.UserID != nil {
		if err := s.userService.UpdateUserStats(*req.UserID, "uploads", 1); err != nil {
			logger.Warn("update user uploads stat failed", zap.Error(err), zap.Uint("user_id", *req.UserID))
		}
		if err := s.userService.UpdateUserStats(*req.UserID, "storage", totalSize); err != nil {
			logger.Warn("update user storage stat failed", zap.Error(err), zap.Uint("user_id", *req.UserID))
		}
	}

	// pending 策略（fail-closed 同单文件通道）
	if filePending {
		target := model.StatusPendingReview
		if _, err := s.SetShareStatus(ctx, []uint{fileCode.ID}, target); err != nil {
			target = model.StatusBlocked
			if _, berr := s.SetShareStatus(ctx, []uint{fileCode.ID}, target); berr != nil {
				logger.Error("multi pending fallback blocked failed, removing share", zap.String("code", fileCode.Code), zap.Error(berr))
				_ = s.fileRepo().SoftDeleteByFileCodeIDs(ctx, []uint{fileCode.ID})
				_ = s.fileCodeRepo.Delete(ctx, fileCode.ID)
				return nil, &ContentRejectedError{}
			}
		}
		if s.flagEmitter != nil {
			s.flagEmitter.EmitShareFlagged(fileCode.Code, "file flagged by moderator", req.OwnerIP)
		}
	}

	channel := req.Channel
	if channel == "" {
		channel = req.UploadType
	}
	metrics.RecordUploadBytes(channel, totalSize)
	metrics.RecordShareCreated(req.UploadType)

	return s.modelToResp(fileCode), nil
}

// ListShareFiles 取分享的文件列表（取件端展示/下载入口）。
// 旧单文件分享无子表行：合成一条虚拟行（主表字段），保证前端统一渲染。
func (s *Service) ListShareFiles(ctx context.Context, code string) ([]*ShareFileItem, error) {
	s.ensureRepository()
	fc, err := s.fileCodeRepo.GetByCode(ctx, code)
	if err != nil {
		return nil, err
	}
	return s.shareFileItems(ctx, fc)
}

// shareFileItems 内部：FileCode → 文件项列表（子表优先，旧数据回退主表）
func (s *Service) shareFileItems(ctx context.Context, fc *model.FileCode) ([]*ShareFileItem, error) {
	children, err := s.fileRepo().ListByFileCodeID(ctx, fc.ID)
	if err != nil {
		return nil, err
	}
	items := make([]*ShareFileItem, 0, len(children))
	for _, c := range children {
		items = append(items, &ShareFileItem{
			ID:       c.ID,
			Name:     c.DisplayName(),
			Size:     c.Size,
			FileHash: c.FileHash,
		})
	}
	if len(items) == 0 && fc.GetFilePath() != "" {
		// 旧单文件分享：无子表行，用主表合成
		items = append(items, &ShareFileItem{
			ID:       0,
			Name:     legacyFileNameOf(fc),
			Size:     fc.Size,
			FileHash: fc.FileHash,
		})
	}
	return items, nil
}

// GetShareChild 取分享内的单个子文件（校验归属，防跨分享枚举文件 ID）。
// legacy 虚拟行（id=0）返回主表合成的子项。
func (s *Service) GetShareChild(ctx context.Context, code string, fileID uint) (*model.FileCode, *model.FileCodeFile, error) {
	s.ensureRepository()
	fc, err := s.fileCodeRepo.GetByCode(ctx, code)
	if err != nil {
		return nil, nil, err
	}
	if fileID == 0 {
		return fc, nil, nil // legacy：走主表字段下载
	}
	child, err := s.fileRepo().GetByID(ctx, fileID)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, nil, errors.New("文件不存在")
		}
		return nil, nil, err
	}
	if child.FileCodeID != fc.ID {
		return nil, nil, errors.New("文件不属于该分享")
	}
	return fc, child, nil
}

// deletePhysicalChildren 删除分享的全部子文件（物理 + 软删子行）。
// 物理删除失败记日志不阻断（与既有删除语义一致，孤儿由 janitor 对账）。
func (s *Service) deletePhysicalChildren(ctx context.Context, fileCodeID uint) {
	children, err := s.fileRepo().ListByFileCodeID(ctx, fileCodeID)
	if err != nil {
		logger.Warn("list children for delete failed", zap.Uint("file_code_id", fileCodeID), zap.Error(err))
		return
	}
	if s.storage == nil {
		return
	}
	for _, c := range children {
		if c.FilePath == "" {
			continue
		}
		if err := s.storage.DeleteFile(ctx, c.FilePath); err != nil {
			logger.Warn("delete child physical file failed",
				zap.String("path", c.FilePath), zap.Error(err))
		}
	}
	if err := s.fileRepo().SoftDeleteByFileCodeIDs(ctx, []uint{fileCodeID}); err != nil {
		logger.Warn("soft delete child rows failed", zap.Uint("file_code_id", fileCodeID), zap.Error(err))
	}
}

// legacyFileNameOf 旧分享展示名（UUIDFileName → Text 存的原始名 → 路径末段）
func legacyFileNameOf(fc *model.FileCode) string {
	if fc.UUIDFileName != "" {
		return fc.UUIDFileName
	}
	if fc.Text != "" {
		return fc.Text
	}
	return uniqueNameOf(fc.GetFilePath())
}

// uniqueNameOf 相对路径末段（唯一文件名）
func uniqueNameOf(relPath string) string {
	for i := len(relPath) - 1; i >= 0; i-- {
		if relPath[i] == '/' {
			return relPath[i+1:]
		}
	}
	return relPath
}
