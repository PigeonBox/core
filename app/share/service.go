package share

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/filescodebox/contracts/errcode"
	"github.com/filescodebox/core/app/moderation"
	"github.com/filescodebox/core/pkg/logger"
	"github.com/filescodebox/core/pkg/metrics"
	"github.com/filescodebox/core/pkg/utils"
	"github.com/filescodebox/core/repo/db"
	"github.com/filescodebox/core/repo/db/dao"
	"github.com/filescodebox/core/repo/db/model"
	"github.com/filescodebox/core/storage"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

type ShareTextReq struct {
	Text         string
	ExpiredAt    *time.Time
	ExpiredCount int
	RequireAuth  bool
	PasswordHash string
	UserID       *uint
	UploadType   string
	OwnerIP      string
	Encrypted    bool   // E2E：Text 为客户端密文（P1）
	CustomCode   string // 自定义取件码（P3；登录用户专属，handler 把关）
}

type ShareFileReq struct {
	Channel      string // 上传通道（direct/chunk/presign/anonymous，metrics 用；可空）
	FilePath     string
	Size         int64
	Text         string
	ExpiredAt    *time.Time
	ExpiredCount int
	RequireAuth  bool
	PasswordHash string
	UserID       *uint
	UploadType   string
	OwnerIP      string
	FileHash     string
	IsChunked    bool
	UploadID     string
	Encrypted    bool   // E2E：存储内容为客户端密文（P1）
	CustomCode   string // 自定义取件码（P3；登录用户专属，handler 把关）
}

type ShareResp struct {
	ID           uint       `json:"id"`
	Code         string     `json:"code"`
	Prefix       string     `json:"prefix"`
	Suffix       string     `json:"suffix"`
	UUIDFileName string     `json:"uuid_file_name"`
	FilePath     string     `json:"file_path"`
	Size         int64      `json:"size"`
	Text         string     `json:"text"`
	ExpiredAt    *time.Time `json:"expired_at"`
	ExpiredCount int        `json:"expired_count"`
	UsedCount    int        `json:"used_count"`
	FileHash     string     `json:"file_hash"`
	IsChunked    bool       `json:"is_chunked"`
	UploadID     string     `json:"upload_id"`
	UserID       *uint      `json:"user_id"`
	UploadType   string     `json:"upload_type"`
	RequireAuth  bool       `json:"require_auth"`
	Encrypted    bool       `json:"encrypted"` // E2E 客户端加密标记（P1）
	OwnerIP      string     `json:"owner_ip"`
	Status       string     `json:"status"` // 管控状态（normal/blocked/pending_review）
	ShareURL     string     `json:"share_url"`      // 相对分享链接
	FullShareURL string     `json:"full_share_url"` // 完整分享链接
}

type Service struct {
	fileCodeRepo *dao.FileCodeRepository
	fileFileRepo *dao.FileCodeFileRepository // 多文件子表 DAO（惰性初始化）
	userService  UserServiceInterface
	storage      storage.StorageInterface
	baseURL      string // 基础 URL，用于生成分享链接
	notifySvc    NotifyServiceInterface
	quotaChecker QuotaChecker
	moderator    moderation.Moderator // 内容审核钩子（nil = 不审核）
	flagEmitter  FlagEventEmitter     // share.flagged webhook（nil = 不推送）
}

// NotifyServiceInterface 取件通知接口（避免 share → notify 直接依赖）
type NotifyServiceInterface interface {
	CreateForUserSimple(ctx context.Context, userID uint, title, content, notifyType, level string) error
}

// UserServiceInterface 定义用户服务接口，避免循环依赖
type UserServiceInterface interface {
	UpdateUserStats(userID uint, statsType string, value int64) error
	// GetUploadSizeCap 生效的单次上传大小上限（管理员可为单用户降限；0=不限）
	GetUploadSizeCap(ctx context.Context, userID uint) int64
}

// QuotaChecker 存储配额检查接口（bootstrap 注入 user service 实现）
type QuotaChecker interface {
	CheckQuota(ctx context.Context, userID uint, addBytes int64) error
}

// FlagEventEmitter share.flagged 事件推送接口（bootstrap 注入 notify service 实现）
type FlagEventEmitter interface {
	EmitShareFlagged(code, reason, ownerIP string)
}

// SetModerator 注入内容审核钩子（bootstrap 调用；nil = 不审核）
func (s *Service) SetModerator(m moderation.Moderator) { s.moderator = m }

// SetFlagEventEmitter 注入 share.flagged webhook 推送（bootstrap 调用）
func (s *Service) SetFlagEventEmitter(e FlagEventEmitter) { s.flagEmitter = e }

// ContentRejectedError 内容未通过审核（moderation 命中且策略为 reject）
type ContentRejectedError struct{}

func (e *ContentRejectedError) Error() string { return "内容未通过安全审核，禁止分享" }

func (e *ContentRejectedError) ErrCode() int { return errcode.CodeContentRejected }

// ShareBlockedError 分享处于管控拒绝态（管理员禁用 / 待审核）。
// handler 侧按 ErrCode 透传（20012 blocked / 20013 pending_review）。
type ShareBlockedError struct {
	Status string
}

func (e *ShareBlockedError) Error() string {
	if e.Status == model.StatusPendingReview {
		return "分享内容待审核，暂不可取件"
	}
	return "分享已被管理员禁用"
}

func (e *ShareBlockedError) ErrCode() int {
	if e.Status == model.StatusPendingReview {
		return errcode.CodeSharePendingReview
	}
	return errcode.CodeShareBlocked
}

// SetShareStatus 管理员设置分享管控状态（禁用/恢复/待审），返回受影响行数。
func (s *Service) SetShareStatus(ctx context.Context, ids []uint, status string) (int64, error) {
	s.ensureRepository()
	return s.fileCodeRepo.UpdateStatusByIDs(ctx, ids, status)
}

// IsTextShare 判定是否纯文本分享：Text 非空且无文件路径。
//
// 回归要点（P0）：ShareFile 会把原始文件名存进 Text 字段，因此仅凭
// Text != "" 判定会把文件分享误判为文本——文件下载曾被文本分支拦截，
// 返回文件名字符串而非文件内容（smoke 只测文本分享所以长期未暴露）。
func IsTextShare(fc *model.FileCode) bool {
	return fc != nil && fc.Text != "" && fc.FilePath == ""
}

func NewService(baseURL string, storageService storage.StorageInterface) *Service {
	// 延迟初始化 repository，确保数据库已经准备好
	return &Service{
		fileCodeRepo: nil, // 延迟初始化
		userService:  nil,
		storage:      storageService,
		baseURL:      baseURL,
		notifySvc:    nil,
	}
}

// ensureRepository 确保repository已初始化
func (s *Service) ensureRepository() {
	if s.fileCodeRepo == nil {
		s.fileCodeRepo = dao.NewFileCodeRepository()
	}
}

func (s *Service) SetUserService(userService UserServiceInterface) {
	s.userService = userService
}

// SetNotifyService 注入 notify service（取件时给 owner 发通知）
func (s *Service) SetNotifyService(svc NotifyServiceInterface) {
	s.notifySvc = svc
}

// SetQuotaChecker 注入存储配额检查器（bootstrap 注入 user service 实现；
// 未注入时上传不检查配额——保持向后兼容）
func (s *Service) SetQuotaChecker(qc QuotaChecker) {
	s.quotaChecker = qc
}

// checkQuota 登录用户上传前配额检查（匿名上传无配额语义，跳过）
func (s *Service) checkQuota(ctx context.Context, userID *uint, addBytes int64) error {
	if s.quotaChecker == nil || userID == nil {
		return nil
	}
	return s.quotaChecker.CheckQuota(ctx, *userID, addBytes)
}

// validCustomCode 自定义取件码合法性（P3）：3-32 位字母/数字/-/_
func validCustomCode(code string) bool {
	if len(code) < 3 || len(code) > 32 {
		return false
	}
	for _, r := range code {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '_':
		default:
			return false
		}
	}
	return true
}

// createWithCode 带可选自定义码的写库（CustomCode 合法时直接占用；冲突报错不重试；
// 空/非法时回退随机码）。customCode 仅登录用户可指定（handler 侧把关，防匿名抢注）。
func (s *Service) createWithCode(ctx context.Context, customCode string, build func(code string) *model.FileCode) (*model.FileCode, error) {
	if customCode == "" {
		return s.createWithRetry(ctx, build)
	}
	if !validCustomCode(customCode) {
		return nil, errors.New("自定义取件码需为 3-32 位字母、数字、- 或 _")
	}
	fc := build(customCode)
	if err := s.fileCodeRepo.Create(ctx, fc); err != nil {
		if errors.Is(err, gorm.ErrDuplicatedKey) {
			return nil, errors.New("自定义取件码已被占用，请换一个")
		}
		return nil, err
	}
	return fc, nil
}

// GenerateCode 生成分享代码（crypto/rand，8 位字母数字）。
func (s *Service) GenerateCode() string {
	const charset = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789"
	const length = 8
	max := big.NewInt(int64(len(charset)))
	b := make([]byte, length)
	for i := range b {
		n, err := rand.Int(rand.Reader, max)
		if err != nil {
			// 极端回退（crypto/rand 几乎不会失败）
			n = big.NewInt(int64(time.Now().UnixNano()) % int64(len(charset)))
		}
		b[i] = charset[n.Int64()]
	}
	return string(b)
}

// createWithRetry 通用写库重试（code 唯一冲突时换码重试，最多 5 次）。
func (s *Service) createWithRetry(ctx context.Context, build func(code string) *model.FileCode) (*model.FileCode, error) {
	for i := 0; i < 5; i++ {
		fc := build(s.GenerateCode())
		if err := s.fileCodeRepo.Create(ctx, fc); err != nil {
			if errors.Is(err, gorm.ErrDuplicatedKey) {
				continue // 换 code 重试
			}
			return nil, err
		}
		return fc, nil
	}
	return nil, errors.New("生成分享码失败：多次冲突")
}

// ShareText 分享文本
func (s *Service) ShareText(ctx context.Context, req *ShareTextReq) (*ShareResp, error) {
	s.ensureRepository()

	fileCode, err := s.createWithCode(ctx, req.CustomCode, func(code string) *model.FileCode {
		return &model.FileCode{
			Code:         code,
			Text:         req.Text,
			ExpiredAt:    req.ExpiredAt,
			ExpiredCount: req.ExpiredCount,
			RequireAuth:  req.RequireAuth,
			PasswordHash: req.PasswordHash,
			UserID:       req.UserID,
			UploadType:   req.UploadType,
			OwnerIP:      req.OwnerIP,
			Encrypted:    req.Encrypted,
		}
	})
	if err != nil {
		return nil, err
	}

	// 更新用户统计
	if s.userService != nil && req.UserID != nil {
		if err := s.userService.UpdateUserStats(*req.UserID, "uploads", 1); err != nil {
			logger.Warn("update user uploads stat failed", zap.Error(err), zap.Uint("user_id", *req.UserID))
		}
	}

	return s.modelToResp(fileCode), nil
}

// ShareTextWithAuth 带认证的文本分享（用于 Handler）。
// requireAuth=true 时必须提供非空 passwordHash(handler 侧负责哈希),
// 传入空哈希会被拒绝,防止创建出"密码保护形同虚设"的分享。
// encrypted=true 时 text 为客户端密文（P1 E2E），跳过服务端转义与敏感词审核。
func (s *Service) ShareTextWithAuth(ctx context.Context, text string, expireValue int, expireStyle string, requireAuth bool, passwordHash string, userID *uint, ownerIP string, encrypted bool, customCode string) (*ShareResp, error) {
	if requireAuth && passwordHash == "" {
		return nil, errors.New("开启密码保护时必须提供密码")
	}
	// 文本大小上限（service 层兜底，handler 已先行校验）
	if maxBytes := utils.GetTextShareMaxBytes(); maxBytes > 0 && int64(len(text)) > maxBytes {
		return nil, fmt.Errorf("%w（上限 %d 字节）", utils.ErrTextTooLarge, maxBytes)
	}

	// 内容审核钩子（治理 2026-10-03）：reject 建分享前拦截；pending 建分享后置待审。
	// E2E 密文分享跳过（服务端零知识，无明文可审）。
	pendingReview := false
	if !encrypted && s.moderator != nil {
		switch s.moderator.InspectText(ctx, text) {
		case moderation.VerdictReject:
			metrics.RecordModerationHit("reject")
			metrics.RecordRejected(metrics.RejectModerated)
			return nil, &ContentRejectedError{}
		case moderation.VerdictPending:
			metrics.RecordModerationHit("pending")
			pendingReview = true
		}
	}

	// 计算过期时间
	expireTime := utils.CalculateExpireTime(expireValue, expireStyle)
	expireCount := utils.CalculateExpireCount(expireStyle, expireValue)

	uploadType := "anonymous"
	if userID != nil {
		uploadType = "authenticated"
	}

	req := &ShareTextReq{
		Text:         text,
		ExpiredAt:    expireTime,
		ExpiredCount: expireCount,
		RequireAuth:  requireAuth,
		PasswordHash: passwordHash,
		UserID:       userID,
		UploadType:   uploadType,
		OwnerIP:      ownerIP,
		Encrypted:    encrypted,
		CustomCode:   customCode, // 登录用户自定义取件码（匿名传空）
	}

	resp, err := s.ShareText(ctx, req)
	if err != nil {
		return nil, err
	}

	// pending 策略：建分享成功后置 pending_review（取件路径拒绝），并推 share.flagged。
	// fail-closed：置待审失败时回退为 blocked——审核拦截宁可误禁也不可静默放行。
	if pendingReview {
		target := model.StatusPendingReview
		if _, err := s.SetShareStatus(ctx, []uint{resp.ID}, target); err != nil {
			logger.Warn("set pending_review failed, fallback to blocked", zap.String("code", resp.Code), zap.Error(err))
			target = model.StatusBlocked
			if _, berr := s.SetShareStatus(ctx, []uint{resp.ID}, target); berr != nil {
				logger.Error("pending fallback blocked failed, removing share", zap.String("code", resp.Code), zap.Error(berr))
				_ = s.fileCodeRepo.Delete(ctx, resp.ID) // 文本分享无物理文件，直接删记录
			}
		}
		resp.Status = target // resp 是写库前快照，回填给调用方
		if s.flagEmitter != nil {
			s.flagEmitter.EmitShareFlagged(resp.Code, "text sensitive word", ownerIP)
		}
	}

	// 生成分享 URL
	resp.ShareURL = fmt.Sprintf("/share/%s", resp.Code)
	resp.FullShareURL = fmt.Sprintf("%s/share/%s", s.baseURL, resp.Code)

	return resp, nil
}

// ShareFile 分享文件
func (s *Service) ShareFile(ctx context.Context, req *ShareFileReq) (*ShareResp, error) {
	return s.CreateShare(ctx, req)
}

// CreateShare 创建分享记录（ShareFile 的语义化别名，便于其他 service 调用）
// 行为：单用户上传上限检查 → 存储配额检查 → 生成 code → 写 file_codes 表 → 返回 share_code / url
func (s *Service) CreateShare(ctx context.Context, req *ShareFileReq) (*ShareResp, error) {
	s.ensureRepository()

	// 单用户单次上传上限（users.max_upload_size 接线，0=不限；匿名无此约束）
	if req.UserID != nil && s.userService != nil {
		if capSize := s.userService.GetUploadSizeCap(ctx, *req.UserID); capSize > 0 {
			if err := utils.CheckUploadSize(req.Size, capSize); err != nil {
				return nil, fmt.Errorf("单次上传大小超过限制（上限 %d 字节）", capSize)
			}
		}
	}

	// 存储配额强制执行（此前字段存在但从未生效）
	if err := s.checkQuota(ctx, req.UserID, req.Size); err != nil {
		metrics.RecordRejected(metrics.RejectQuota)
		return nil, err
	}

	// 文件审核钩子（治理）：直传/分片/秒传三通道共用本入口。
	// reject 建分享前拦截；pending 建分享后置待审并推事件（与文本通道同策略）。
	filePending := false
	if s.moderator != nil {
		meta := moderation.UploadMeta{
			FileName:    req.Text, // 直传/分片把原始文件名存 Text
			Size:        req.Size,
			OwnerIP:     req.OwnerIP,
			UserID:      req.UserID,
			UploadID:    req.UploadID,
			Channel:     req.Channel,
			StoragePath: req.FilePath, // 文件内容扫描（ClamAV）读取路径
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

	fileCode, err := s.createWithCode(ctx, req.CustomCode, func(code string) *model.FileCode {
		return &model.FileCode{
			Code:         code,
			FilePath:     req.FilePath,
			Size:         req.Size,
			Text:         req.Text,
			ExpiredAt:    req.ExpiredAt,
			ExpiredCount: req.ExpiredCount,
			RequireAuth:  req.RequireAuth,
			PasswordHash: req.PasswordHash,
			UserID:       req.UserID,
			UploadType:   req.UploadType,
			OwnerIP:      req.OwnerIP,
			FileHash:     req.FileHash,
			IsChunked:    req.IsChunked,
			UploadID:     req.UploadID,
			Encrypted:    req.Encrypted,
		}
	})
	if err != nil {
		return nil, err
	}

	// 子表同步写一行（P0 多文件：新旧分享统一经子表读取；失败仅降级为主表读取，不阻断）
	if fc := fileCode; fc != nil {
		if rel := fc.GetFilePath(); rel != "" {
			child := &model.FileCodeFile{
				FileCodeID:   fc.ID,
				FileName:     req.Text,
				UUIDFileName: fc.UUIDFileName,
				FilePath:     rel,
				Size:         req.Size,
				FileHash:     req.FileHash,
			}
			if err := s.fileRepo().CreateBatch(ctx, []*model.FileCodeFile{child}); err != nil {
				logger.Warn("write child file row failed (fallback to legacy fields)",
					zap.String("code", fc.Code), zap.Error(err))
			}
		}
	}

	// 更新用户统计
	if s.userService != nil && req.UserID != nil {
		if err := s.userService.UpdateUserStats(*req.UserID, "uploads", 1); err != nil {
			logger.Warn("update user uploads stat failed", zap.Error(err), zap.Uint("user_id", *req.UserID))
		}
		if err := s.userService.UpdateUserStats(*req.UserID, "storage", req.Size); err != nil {
			logger.Warn("update user storage stat failed", zap.Error(err), zap.Uint("user_id", *req.UserID))
		}
	}

	// 文件 pending 策略（fail-closed 同文本通道）
	if filePending {
		target := model.StatusPendingReview
		if _, err := s.SetShareStatus(ctx, []uint{fileCode.ID}, target); err != nil {
			target = model.StatusBlocked
			if _, berr := s.SetShareStatus(ctx, []uint{fileCode.ID}, target); berr != nil {
				logger.Error("file pending fallback blocked failed, removing share", zap.String("code", fileCode.Code), zap.Error(berr))
				_ = s.fileCodeRepo.Delete(ctx, fileCode.ID)
				return nil, &ContentRejectedError{}
			}
		}
		if s.flagEmitter != nil {
			s.flagEmitter.EmitShareFlagged(fileCode.Code, "file flagged by moderator", req.OwnerIP)
		}
	}

	// 业务 metrics（治理）：channel 缺省按 upload_type 记
	channel := req.Channel
	if channel == "" {
		channel = req.UploadType
	}
	metrics.RecordUploadBytes(channel, req.Size)
	metrics.RecordShareCreated(req.UploadType)

	return s.modelToResp(fileCode), nil
}

// GetFileByCode 通过代码获取文件
func (s *Service) GetFileByCode(ctx context.Context, code string) (*model.FileCode, error) {
	s.ensureRepository()

	fileCode, err := s.fileCodeRepo.GetByCode(ctx, code)
	if err != nil {
		return nil, err
	}

	// 检查文件是否过期
	if fileCode.IsExpired() {
		return nil, errors.New("file has expired")
	}

	// 管控状态：blocked / pending_review 拒绝取件（typed error，handler 按业务码透传）
	if fileCode.IsBlockedShare() {
		metrics.RecordRejected(metrics.RejectBlocked)
		return nil, &ShareBlockedError{Status: fileCode.Status}
	}

	return fileCode, nil
}

// GetFilesByUserID 获取用户的文件列表
func (s *Service) GetFilesByUserID(ctx context.Context, userID uint, page, pageSize int) ([]*model.FileCode, int64, error) {
	s.ensureRepository()
	return s.fileCodeRepo.GetFilesByUserIDWithPagination(ctx, userID, page, pageSize)
}

// DeleteFile 删除文件
func (s *Service) DeleteFile(ctx context.Context, fileID uint, userID *uint) error {
	s.ensureRepository()

	// 如果指定了用户ID，验证文件所有权
	if userID != nil {
		file, err := s.fileCodeRepo.GetByUserID(ctx, *userID, fileID)
		if err != nil {
			return err
		}

		// 更新用户统计（减少存储空间）
		if s.userService != nil && userID != nil {
			if err := s.userService.UpdateUserStats(*userID, "storage", -file.Size); err != nil {
				logger.Warn("update user storage stat failed on delete", zap.Error(err), zap.Uint("user_id", *userID))
			}
		}
	}

	return s.fileCodeRepo.Delete(ctx, fileID)
}

// DeleteFileByCode 根据分享码删除文件。
//
// 删除顺序（保证一致性）：
//  1. 查所有权
//  2. 删 DB 记录（事务原子）
//  3. 事务提交后删物理文件（不可回滚，失败记日志不阻断）
//  4. 扣减用户 storage 统计（best-effort，失败记日志）
//
// 关键：先删 DB 再删物理，避免"物理删了但 DB 没删"的孤儿反向场景。
func (s *Service) DeleteFileByCode(ctx context.Context, code string, userID uint) error {
	s.ensureRepository()

	// 1. 根据 code 查询文件记录
	file, err := s.fileCodeRepo.GetByCode(ctx, code)
	if err != nil {
		return fmt.Errorf("分享不存在")
	}

	// 2. 验证文件所有权（userID匹配）
	if file.UserID == nil || *file.UserID != userID {
		return fmt.Errorf("无权限删除此分享")
	}

	// 3. 删除数据库记录（事务）
	if err := db.GetDB().WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return tx.Delete(&model.FileCode{}, file.ID).Error
	}); err != nil {
		return fmt.Errorf("删除分享记录失败: %w", err)
	}

	// 4. 事务提交成功后，删除物理文件（不可回滚，放事务外，失败记日志不阻断）
	//    多文件分享：子表各文件一并删除（legacy 单文件走主表路径）
	if s.storage != nil {
		s.deletePhysicalChildren(ctx, file.ID)
		if file.FilePath != "" && file.UUIDFileName != "" {
			filePath := file.GetFilePath()
			if filePath != "" {
				if err := s.storage.DeleteFile(ctx, filePath); err != nil {
					logger.Warn("delete physical file failed", zap.String("path", filePath), zap.Error(err))
				}
			}
		}
	}

	// 5. 扣减用户 storage 统计（best-effort，失败记日志）
	if s.userService != nil {
		if err := s.userService.UpdateUserStats(userID, "storage", -file.Size); err != nil {
			logger.Warn("update user storage stat failed on delete",
				zap.Uint("user_id", userID), zap.Int64("size", file.Size), zap.Error(err))
		}
	}

	return nil
}

// GetFileList 获取文件列表
func (s *Service) GetFileList(ctx context.Context, page, pageSize int, search string) ([]*model.FileCode, int64, error) {
	s.ensureRepository()
	return s.fileCodeRepo.List(ctx, page, pageSize, search)
}

// UpdateFileUsage 原子扣减剩余次数（DB 为准，防并发超卖）。
// 返回 ok=true 表示扣减成功（可下载）；ok=false 表示已耗尽。
func (s *Service) UpdateFileUsage(ctx context.Context, code string) (bool, error) {
	s.ensureRepository()
	return s.fileCodeRepo.DecrementExpiredCount(ctx, code)
}

// GetFileWithUsage 获取文件并校验密码（不扣次数，扣次数由下载链路调 UpdateFileUsage）。
// viewerIP 由 handler 从可信解析 middleware.ClientIP(c) 注入。
// authedByToken：调用方已校验有效下载令牌（令牌由取件查询在密码/取件校验通过后
// 签发，等价于已认证——否则密码保护分享"凭令牌下载"还要再输一次密码，令牌失效）。
func (s *Service) GetFileWithUsage(ctx context.Context, code, password, viewerIP string, authedByToken bool) (*model.FileCode, error) {
	s.ensureRepository()

	fileCode, err := s.GetFileByCode(ctx, code)
	if err != nil {
		return nil, err
	}

	// 真实密码校验（替代原 TODO：仅检查非空）；持有效下载令牌视为已认证
	if fileCode.RequireAuth && !authedByToken {
		// 防御历史脏数据:require_auth=true 但哈希缺失 → 一律拒绝,
		// 而非以"密码错误"之外的方式放行(CheckPassword 对空哈希已收紧为全拒)
		if fileCode.PasswordHash == "" {
			return nil, errors.New("该分享的密码保护配置异常，请联系分享者")
		}
		if !utils.CheckPassword(fileCode.PasswordHash, password) {
			return nil, errors.New("密码错误")
		}
	}

	// 记录取件人 + 通知 owner（fire-and-forget，recover 防 panic 影响进程）
	go func() {
		defer func() { _ = recover() }()
		_ = s.RecordViewerAndNotify(context.Background(), code, viewerIP, "")
	}()

	return fileCode, nil
}

// RecordViewerAndNotify 记录取件人 + 给 owner 发通知。
//   - viewerIP: 取件人 IP（由 handler 从可信解析 middleware.ClientIP(c) 注入）
//   - viewerDetail: 额外通知内容
//
// 通知去重：同 code 5 分钟内只通知一次（用 LastNotifiedAt 字段）。
// 若 file_codes 找不到，静默跳过。
func (s *Service) RecordViewerAndNotify(ctx context.Context, code, viewerIP, viewerDetail string) error {
	s.ensureRepository()
	if err := s.fileCodeRepo.UpdateViewer(ctx, code, viewerIP); err != nil {
		// 文件可能不存在，记日志后跳过
		logger.Warn("update viewer failed", zap.String("code", code), zap.Error(err))
		return nil
	}
	// 读最新记录判断 owner
	fc, err := s.fileCodeRepo.GetByCode(ctx, code)
	if err != nil || fc == nil {
		return nil
	}
	if fc.UserID == nil || s.notifySvc == nil {
		return nil
	}
	// 通知去重：同 code 5 分钟内只通知一次
	if fc.LastNotifiedAt != nil && time.Since(*fc.LastNotifiedAt) < 5*time.Minute {
		return nil
	}
	// 更新 LastNotifiedAt（先更新，避免并发重复通知）
	now := time.Now()
	if err := s.fileCodeRepo.UpdateColumns(ctx, fc.ID, map[string]interface{}{"last_notified_at": now}); err != nil {
		logger.Warn("update last_notified_at failed", zap.Uint("id", fc.ID), zap.Error(err))
	}

	title := "您的分享已被取件"
	if IsTextShare(fc) {
		title = "您的文本分享已被查看"
	}
	content := fmt.Sprintf("分享码: %s\n取件人 IP: %s\n时间: %s", code, viewerIP, now.Format("2006-01-02 15:04:05"))
	if viewerDetail != "" {
		content += "\n" + viewerDetail
	}
	return s.notifySvc.CreateForUserSimple(ctx, *fc.UserID, title, content, "share_retrieved", "info")
}

// modelToResp 将模型转换为响应
func (s *Service) modelToResp(fileCode *model.FileCode) *ShareResp {
	status := fileCode.Status
	if status == "" {
		status = model.StatusNormal
	}
	return &ShareResp{
		ID:           fileCode.ID,
		Code:         fileCode.Code,
		Status:       status,
		Prefix:       fileCode.Prefix,
		Suffix:       fileCode.Suffix,
		UUIDFileName: fileCode.UUIDFileName,
		FilePath:     fileCode.FilePath,
		Size:         fileCode.Size,
		Text:         fileCode.Text,
		ExpiredAt:    fileCode.ExpiredAt,
		ExpiredCount: fileCode.ExpiredCount,
		UsedCount:    fileCode.UsedCount,
		FileHash:     fileCode.FileHash,
		IsChunked:    fileCode.IsChunked,
		UploadID:     fileCode.UploadID,
		UserID:       fileCode.UserID,
		UploadType:   fileCode.UploadType,
		RequireAuth:  fileCode.RequireAuth,
		Encrypted:    fileCode.Encrypted,
		OwnerIP:      fileCode.OwnerIP,
	}
}

// UserShareListItem 用户分享列表项（包含 viewer 追踪字段）
type UserShareListItem struct {
	ID           uint       `json:"id"`
	Code         string     `json:"code"`
	Prefix       string     `json:"prefix"`
	Suffix       string     `json:"suffix"`
	FileName     string     `json:"file_name"`
	FilePath     string     `json:"file_path"`
	Size         int64      `json:"size"`
	Text         string     `json:"text"`
	ExpiredAt    *time.Time `json:"expired_at"`
	ExpiredCount int        `json:"expired_count"`
	UsedCount    int        `json:"used_count"`
	RequireAuth  bool       `json:"require_auth"`
	UploadType   string     `json:"upload_type"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
	DeletedAt    *time.Time `json:"deleted_at,omitempty"`
	ViewerIP     string     `json:"viewer_ip"`
	ViewerAt     *time.Time `json:"viewer_at"`
	ViewerCount  int        `json:"viewer_count"`
	IsExpired    bool       `json:"is_expired"`
	IsTextShare  bool       `json:"is_text_share"`
	Status       string     `json:"status"` // 管控状态：owner 有权知道自己被禁用/待审
}

// deletedAtToPtr gorm.DeletedAt → *time.Time（nil 表示未删除）
func deletedAtToPtr(d gorm.DeletedAt) *time.Time {
	if d.Valid {
		return &d.Time
	}
	return nil
}

// ListUserShares 获取用户的分享列表（带筛选）
func (s *Service) ListUserShares(ctx context.Context, userID uint, filter dao.UserShareFilter) ([]*UserShareListItem, int64, error) {
	s.ensureRepository()
	files, total, err := s.fileCodeRepo.GetUserSharesWithFilter(ctx, userID, filter)
	if err != nil {
		return nil, 0, err
	}
	items := make([]*UserShareListItem, 0, len(files))
	for _, f := range files {
		items = append(items, toUserShareListItem(f))
	}
	return items, total, nil
}

// BatchDeleteUserShares 批量软删除
func (s *Service) BatchDeleteUserShares(ctx context.Context, userID uint, codes []string) (int, error) {
	s.ensureRepository()
	return s.fileCodeRepo.BatchSoftDeleteByCodes(ctx, userID, codes)
}

// BatchExtendUserShares 批量延期
func (s *Service) BatchExtendUserShares(ctx context.Context, userID uint, codes []string, newExpireAt *time.Time) (int, error) {
	s.ensureRepository()
	return s.fileCodeRepo.BatchExtendByCodes(ctx, userID, codes, newExpireAt)
}

// RestoreUserShare 恢复软删除的分享
func (s *Service) RestoreUserShare(ctx context.Context, userID uint, code string) error {
	s.ensureRepository()
	return s.fileCodeRepo.RestoreByCode(ctx, userID, code)
}

// HardDeleteUserShare 永久删除软删除的分享
func (s *Service) HardDeleteUserShare(ctx context.Context, userID uint, code string) error {
	s.ensureRepository()
	return s.fileCodeRepo.HardDeleteByCode(ctx, userID, code)
}

// RecordViewer 记录取件人 IP / 时间（用于 owner 查看取件历史）
// 取件流程中调用。返回 fileCode 供后续通知使用
func (s *Service) RecordViewer(ctx context.Context, code, viewerIP string) (*model.FileCode, error) {
	s.ensureRepository()
	if err := s.fileCodeRepo.UpdateViewer(ctx, code, viewerIP); err != nil {
		return nil, err
	}
	return s.fileCodeRepo.GetByCode(ctx, code)
}

// toUserShareListItem model → 列表项
func toUserShareListItem(f *model.FileCode) *UserShareListItem {
	item := &UserShareListItem{
		ID:           f.ID,
		Code:         f.Code,
		Prefix:       f.Prefix,
		Suffix:       f.Suffix,
		FilePath:     f.FilePath,
		Size:         f.Size,
		Text:         f.Text,
		ExpiredAt:    f.ExpiredAt,
		ExpiredCount: f.ExpiredCount,
		UsedCount:    f.UsedCount,
		RequireAuth:  f.RequireAuth,
		UploadType:   f.UploadType,
		CreatedAt:    f.CreatedAt,
		UpdatedAt:    f.UpdatedAt,
		DeletedAt:    deletedAtToPtr(f.DeletedAt),
		ViewerIP:     f.ViewerIP,
		ViewerAt:     f.ViewerAt,
		ViewerCount:  f.ViewerCount,
		IsExpired:    f.IsExpired(),
		Status:       f.Status,
		IsTextShare:  f.Text != "",
	}
	// 提取文件名
	fileName := f.UUIDFileName
	if fileName == "" && f.FilePath != "" {
		parts := strings.Split(f.FilePath, "/")
		if len(parts) > 0 {
			fileName = parts[len(parts)-1]
		}
	}
	item.FileName = fileName
	return item
}
