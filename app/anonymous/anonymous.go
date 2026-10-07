// Package anonymous 实现匿名取件 service（仿 vastsa/PigeonBox UX）。
//
// 核心流程：
//  1. 上传方：上传文件后，调用 GenerateCode 获取 6 位取件码（建立 pickup_code → share_code 映射）
//  2. 取件方：输入 6 位码 + 可选密码，按码取文件
//  3. 系统：校验（DB 为准）→ 扣减次数（DB 原子）→ 返回下载信息
//
// 设计要点（DB 为唯一真相源）：
//   - Redis 仅存 pickup_code → share_code 映射 + 展示信息（文件名等）
//   - 过期时间、剩余次数、密码哈希全部以 file_codes 表为准
//   - 取件码字符表去掉易混淆字符 0/O/1/I/L
package anonymous

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/pigeonbox/contracts/errcode"
	"github.com/pigeonbox/core/pkg/logger"
	"github.com/pigeonbox/core/pkg/utils"
	"github.com/pigeonbox/core/repo/db/dao"
	"github.com/pigeonbox/core/repo/db/model"
	"github.com/pigeonbox/kit/uidgen"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// 6 位取件码字符表（去掉易混淆字符 0/O/1/I/L）
const codeAlphabet = "ABCDEFGHJKMNPQRSTUVWXYZ23456789"
const codeLength = 6

// shareCode 分享码长度（file_codes.code，字母数字混合、区分大小写）
const shareCodeLength = 8

// Redis key 模板（仅存映射 + 展示信息，真实状态查 DB）
const (
	keyPickupCodeMapping = "anon:code:%s" // pickup_code -> share_code
	keyPickupCodeMeta    = "anon:meta:%s" // pickup_code -> 展示信息(share_code|file_name|file_size|content_type|require_auth)
	keyPickupCodeNeg     = "anon:neg:%s"  // 负缓存标记：KV 未命中且 DB 也无此码（防枚举穿透打库）
)

// 回源缓存的 TTL：
//   - 负缓存短（2min）：新分享建好后最多遮蔽 2min（仅影响"建前被探测过同名的
//     自定义码"这一罕见时序，正常取件码走映射键不受影响），自愈。
//   - 回源回填短（5min）：DB 是真相源，回填只是缓存加速；TTL 到期自动回落直查。
const (
	negCacheTTL   = 2 * time.Minute
	dbBackfillTTL = 5 * time.Minute
)

// 错误哨兵
var (
	ErrCodeNotFound  = errors.New("pickup code not found")
	ErrCodeExpired   = errors.New("pickup code expired")
	ErrCodeExhausted = errors.New("pickup code exhausted")
	ErrPasswordWrong = errors.New("password wrong")
	ErrNotReady      = errors.New("share upload incomplete") // 登记占位未回填物理文件
)

// BlockedError 分享处于管控拒绝态（治理状态机）。
// handler 侧 errors.As 后按 ErrCode 透传（20012 blocked / 20013 pending_review）。
type BlockedError struct{ Status string }

func (e *BlockedError) Error() string {
	if e.Status == "pending_review" {
		return "分享内容待审核，暂不可取件"
	}
	return "分享已被管理员禁用"
}

func (e *BlockedError) ErrCode() int {
	if e.Status == "pending_review" {
		return errcode.CodeSharePendingReview
	}
	return errcode.CodeShareBlocked
}

// redisKV 匿名取件所需的最小 Redis 命令集（*redis.Client 天然满足；
// 字段收窄为方法集以便注入内存 mock，构造函数仍收具体客户端）。
type redisKV interface {
	Get(ctx context.Context, key string) *redis.StringCmd
	Set(ctx context.Context, key string, value any, expiration time.Duration) *redis.StatusCmd
	SetArgs(ctx context.Context, key string, value any, a redis.SetArgs) *redis.StatusCmd
	Del(ctx context.Context, keys ...string) *redis.IntCmd
}

// Service 匿名取件 service。
// Redis 仅存映射 + 展示信息；过期/次数/密码等真实状态全部以 file_codes 表为准。
type Service struct {
	rdb          redisKV
	fileCodeRepo *dao.FileCodeRepository
}

// NewService 创建 service。fileCodeRepo 为 nil 时内部自建（Retrieve 需查 DB）。
// rdb nil 归一化：typed-nil 接口会骗过"Redis 可用"守卫（!= nil 判真）。
// rdb 为 nil（redis.host 未配置）时进入单机内存模式：映射/展示信息存进程内
// TTL KV，匿名取件全功能可用（单进程语义等价；重启丢失、不跨副本共享）。
func NewService(rdb *redis.Client, fileCodeRepo *dao.FileCodeRepository) *Service {
	if fileCodeRepo == nil {
		fileCodeRepo = dao.NewFileCodeRepository()
	}
	var kv redisKV
	if rdb != nil {
		kv = rdb
	} else {
		kv = newMemoryKV()
	}
	return &Service{rdb: kv, fileCodeRepo: fileCodeRepo}
}

// CodeMeta 取件码展示信息（仅存于 Redis，真实状态查 DB）
type CodeMeta struct {
	ShareCode   string // 真实 file_code
	FileName    string
	FileSize    int64
	ContentType string
	RequireAuth bool // 仅展示"是否需要密码"
}

// GenerateCode 生成 6 位取件码，建立 pickup_code → share_code 映射。
// expireAt 决定 Redis key 的 TTL（应与 DB 记录过期时间对齐）。
func (s *Service) GenerateCode(ctx context.Context, meta CodeMeta, expireAt time.Time) (string, error) {
	if s.rdb == nil {
		return "", errors.New("redis 未配置，匿名取件功能不可用")
	}
	ttl := time.Until(expireAt)
	if ttl <= 0 {
		return "", errors.New("expireAt 已过期")
	}
	for i := 0; i < 10; i++ {
		code := randomCode()
		// SetNX 已废弃（SA1019），改用 Set + NX 选项；NX 且键已存在时返回 redis.Nil
		key := fmt.Sprintf(keyPickupCodeMapping, code)
		_, err := s.rdb.SetArgs(ctx, key, meta.ShareCode, redis.SetArgs{TTL: ttl, Mode: "NX"}).Result()
		if errors.Is(err, redis.Nil) {
			continue // 已存在，重试
		}
		if err != nil {
			return "", err
		}
		metaStr := fmt.Sprintf("%s|%s|%d|%s|%t",
			meta.ShareCode, meta.FileName, meta.FileSize, meta.ContentType, meta.RequireAuth)
		if err := s.rdb.Set(ctx, fmt.Sprintf(keyPickupCodeMeta, code), metaStr, ttl).Err(); err != nil {
			s.rdb.Del(ctx, fmt.Sprintf(keyPickupCodeMapping, code))
			return "", err
		}
		return code, nil
	}
	return "", errors.New("failed to generate unique code after 10 retries")
}

// lookupShareCode 把用户输入解析为分享码：
// 6 位输入按取件码处理（容忍小写，规范化后查映射）；
// 映射未命中且输入形如分享码时回源 DB 直查——
// 文本分享没有取件码（不写 KV），用户手里只有分享成功弹窗里的 8 位码。
// 回源路径带双向缓存：命中回填映射（短 TTL，重复查询不再打库）；
// DB 也无此码则放负缓存标记（短 TTL，防取件码枚举穿透打库）。
// 真实状态（过期/次数/密码）始终以 DB 为准，缓存仅加速"码→分享码"解析。
func (s *Service) lookupShareCode(ctx context.Context, code string) (string, error) {
	trimmed := strings.TrimSpace(code)
	if len(trimmed) == codeLength {
		trimmed = strings.ToUpper(trimmed)
	}
	mappingKey := fmt.Sprintf(keyPickupCodeMapping, trimmed)
	shareCode, err := s.rdb.Get(ctx, mappingKey).Result()
	if err == nil {
		return shareCode, nil
	}
	if !errors.Is(err, redis.Nil) {
		return "", err
	}
	// 负缓存快速路径：近期已确认"映射与 DB 均无此码"
	if _, err := s.rdb.Get(ctx, fmt.Sprintf(keyPickupCodeNeg, trimmed)).Result(); err == nil {
		return "", ErrCodeNotFound
	}
	if isShareCodeShape(trimmed) {
		fc, dbErr := s.fileCodeRepo.GetByCode(ctx, trimmed)
		if dbErr != nil || fc == nil {
			// 回源 DB 也没有：放负缓存标记
			s.rdb.Set(ctx, fmt.Sprintf(keyPickupCodeNeg, trimmed), "1", negCacheTTL)
			return "", ErrCodeNotFound
		}
		// 回源命中：回填映射缓存（真实状态仍查 DB）
		s.rdb.Set(ctx, mappingKey, fc.Code, dbBackfillTTL)
		return fc.Code, nil
	}
	return "", ErrCodeNotFound
}

// isShareCodeShape 判断输入是否可回退 DB 直查分享码：8 位随机分享码（字母数字，
// 区分大小写）或自定义取件码（3-32 位字母/数字/-/_，与创建端约束一致，上限放宽
// 至 64 留裕量）。此前仅放行恰 8 位——自定义码两头不落，/anonymous/search|retrieve|
// download 恒 404，联邦自定义码跨站程序化取件在最后一公里断裂（v0.11.1 修复）。
func isShareCodeShape(code string) bool {
	if len(code) < 3 || len(code) > 64 {
		return false
	}
	for i := 0; i < len(code); i++ {
		c := code[i]
		if (c < '0' || c > '9') && (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') && c != '-' && c != '_' {
			return false
		}
	}
	return true
}

// enrichMetaFromDB 分享码直查路径没有 Redis 展示信息，用 DB 记录补齐
func enrichMetaFromDB(meta *CodeMeta, fc *model.FileCode) {
	if meta.FileName == "" {
		meta.FileName = fc.DisplayName()
	}
	if meta.FileSize == 0 {
		meta.FileSize = fc.Size
	}
	meta.RequireAuth = fc.RequireAuth
}

// Retrieve 按取件码取件（校验 + 扣减次数，DB 为准）。
// 返回展示信息 CodeMeta。每次成功调用扣减一次剩余次数。
func (s *Service) Retrieve(ctx context.Context, code, password string) (*CodeMeta, error) {
	if s.rdb == nil {
		return nil, errors.New("redis 未配置，匿名取件功能不可用")
	}
	// 1. 解析取件码/分享码 → share_code
	shareCode, err := s.lookupShareCode(ctx, code)
	if err != nil {
		return nil, err
	}

	// 2. 展示信息（兼容旧格式：解析失败用空值）
	metaStr, _ := s.rdb.Get(ctx, fmt.Sprintf(keyPickupCodeMeta, strings.ToUpper(strings.TrimSpace(code)))).Result()
	meta := parseMeta(metaStr, shareCode)

	// 3. 查 DB 真实状态
	fc, err := s.fileCodeRepo.GetByCode(ctx, shareCode)
	if err != nil {
		return nil, ErrCodeNotFound
	}
	enrichMetaFromDB(meta, fc)

	// 4. 校验过期（时间 + 次数）
	if fc.IsExpired() {
		return nil, ErrCodeExpired
	}

	// 4.5 管控状态：blocked / pending_review 拒绝取件
	if fc.IsBlockedShare() {
		return nil, &BlockedError{Status: fc.Status}
	}

	// 4.6 未完成登记（FilePath 空且非文本）：此前会先扣次数再在下载时 500
	if !fc.IsTextShare() && fc.GetFilePath() == "" {
		return nil, ErrNotReady
	}

	// 5. 校验密码（bcrypt，DB 为准）
	if fc.RequireAuth {
		if !utils.CheckPassword(fc.PasswordHash, password) {
			return nil, ErrPasswordWrong
		}
	}

	// 6. 原子扣减次数（DB 为准，防并发超卖）
	ok, err := s.fileCodeRepo.DecrementExpiredCount(ctx, shareCode)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, ErrCodeExhausted
	}

	return meta, nil
}

// Cancel 作废取件码（删 Redis 映射）
func (s *Service) Cancel(ctx context.Context, code string) error {
	return s.cleanup(ctx, code)
}

// Peek 按取件码查询分享信息（不扣次数、不校验密码），仅供展示。
// 返回展示信息 CodeMeta + DB 记录（含剩余次数、过期时间等）。
func (s *Service) Peek(ctx context.Context, code string) (*CodeMeta, *model.FileCode, error) {
	if s.rdb == nil {
		return nil, nil, errors.New("redis 未配置，匿名取件功能不可用")
	}
	shareCode, err := s.lookupShareCode(ctx, code)
	if err != nil {
		return nil, nil, err
	}
	metaStr, _ := s.rdb.Get(ctx, fmt.Sprintf(keyPickupCodeMeta, strings.ToUpper(strings.TrimSpace(code)))).Result()
	meta := parseMeta(metaStr, shareCode)
	fc, err := s.fileCodeRepo.GetByCode(ctx, shareCode)
	if err != nil {
		return nil, nil, ErrCodeNotFound
	}
	enrichMetaFromDB(meta, fc)
	// 过期分享与不存在同语义（统一 404 防探测），与 /share/metadata、Retrieve
	// 对齐——此前仅查封禁不查过期，过期分享的元数据经本端点永久可查
	// （v0.11.1 修复）。
	if fc.IsExpired() {
		return nil, nil, ErrCodeNotFound
	}
	if fc.IsBlockedShare() {
		return nil, nil, &BlockedError{Status: fc.Status}
	}
	return meta, fc, nil
}

// ============ 内部辅助 ============

// randomCode 生成 6 位随机码（crypto/rand 经 utils 统一收口，字符表见 codeAlphabet）
func randomCode() string {
	return uidgen.RandomString(codeAlphabet, codeLength)
}

// parseMeta 解析展示信息（兼容旧格式：字段不足时用空值）
// 新格式: share_code|file_name|file_size|content_type|require_auth
func parseMeta(metaStr, shareCode string) *CodeMeta {
	meta := &CodeMeta{ShareCode: shareCode}
	if metaStr == "" {
		return meta
	}
	parts := splitBy(metaStr, '|', 5)
	if len(parts) >= 2 {
		meta.FileName = parts[1]
	}
	if len(parts) >= 3 {
		_, _ = fmt.Sscanf(parts[2], "%d", &meta.FileSize)
	}
	if len(parts) >= 4 {
		meta.ContentType = parts[3]
	}
	if len(parts) >= 5 {
		meta.RequireAuth = parts[4] == "true"
	}
	return meta
}

// splitBy 简易 split（避免引入 strings.Split 提升可读性）
func splitBy(s string, sep byte, max int) []string {
	result := make([]string, 0, max)
	start, count := 0, 0
	for i := 0; i < len(s); i++ {
		if s[i] == sep && count < max-1 {
			result = append(result, s[start:i])
			start = i + 1
			count++
		}
	}
	result = append(result, s[start:])
	return result
}

func (s *Service) cleanup(ctx context.Context, code string) error {
	return s.rdb.Del(ctx,
		fmt.Sprintf(keyPickupCodeMapping, code),
		fmt.Sprintf(keyPickupCodeMeta, code),
	).Err()
}

// AnonymousShareParams 匿名分享端到端参数
type AnonymousShareParams struct {
	FilePath       string
	FileName       string
	FileSize       int64
	ContentType    string
	ExpireAt       *time.Time // 必填，决定 DB 过期时间 + Redis TTL
	MaxPickupCount int        // -1=无限, 0=默认无限, >0=限制
	Password       string     // 空=无密码
}

// CreateAnonymousShare 端到端：建 file_codes 记录 + 生成取件码。
// 供 gen handler 调用，避免 handler 直接操作 DAO；密码在此 bcrypt 哈希后存 DB。
// 返回 6 位取件码。
func (s *Service) CreateAnonymousShare(ctx context.Context, p AnonymousShareParams) (string, error) {
	if p.ExpireAt == nil {
		return "", errors.New("ExpireAt 必填")
	}
	maxCount := p.MaxPickupCount
	if maxCount == 0 {
		maxCount = -1 // 默认无限
	}

	hash, err := utils.HashPassword(p.Password)
	if err != nil {
		return "", err
	}

	fc := &model.FileCode{
		Code:         randomShareCode(),
		FilePath:     p.FilePath,
		UUIDFileName: p.FileName,
		Size:         p.FileSize,
		ExpiredAt:    p.ExpireAt,
		ExpiredCount: maxCount,
		RequireAuth:  p.Password != "",
		PasswordHash: hash,
		UploadType:   "anonymous",
	}
	if err := s.fileCodeRepo.Create(ctx, fc); err != nil {
		return "", err
	}

	pickupCode, err := s.GenerateCode(ctx, CodeMeta{
		ShareCode:   fc.Code,
		FileName:    p.FileName,
		FileSize:    p.FileSize,
		ContentType: p.ContentType,
		RequireAuth: p.Password != "",
	}, *p.ExpireAt)
	if err != nil {
		// 回滚 DB 记录（物理文件未落库，无需清）
		if dErr := s.fileCodeRepo.Delete(ctx, fc.ID); dErr != nil {
			logger.Warn("rollback file_code on generate code failed", zap.Uint("id", fc.ID), zap.Error(dErr))
		}
		return "", err
	}
	return pickupCode, nil
}

// MintForShare 为既有文件分享铸造 6 位取件码（仅写 KV 映射，不动 DB 记录）。
// 供 app/share 经窄接口在 CreateShare 成功后调用——直传/直传分片/秒传各通道
// 的文件分享统一获得取件码（对齐上游"文件另有 N 位取件码"语义）。
//   - expireAt nil（永久分享）→ 返回空串不铸造：映射 TTL 无法对齐永久语义，
//     永久分享继续用 8 位分享码（lookupShareCode 的 DB 回退路径天然支持）。
//   - KV 写失败返回错误但调用方应视为非致命（出码主流程不阻断）。
func (s *Service) MintForShare(ctx context.Context, shareCode, fileName string, fileSize int64, requireAuth bool, expireAt *time.Time) (string, error) {
	if s.rdb == nil {
		return "", nil
	}
	if expireAt == nil || time.Until(*expireAt) <= 0 {
		return "", nil
	}
	return s.GenerateCode(ctx, CodeMeta{
		ShareCode:   shareCode,
		FileName:    fileName,
		FileSize:    fileSize,
		ContentType: "application/octet-stream",
		RequireAuth: requireAuth,
	}, *expireAt)
}

// ResolvePlaceholder 校验取件码对应一个"待回填"的占位分享（/anonymous/generate
// 创建、FilePath 为空），返回其 8 位分享码。供 presign 直传绑定通道在 Init 时
// 预检、Complete 时定位回填目标。
// 非占位（已完成上传）/不存在/已过期 → 显式错误，防把对象绑到任意分享上。
func (s *Service) ResolvePlaceholder(ctx context.Context, pickupCode string) (string, error) {
	if s.rdb == nil {
		return "", errors.New("redis 未配置，匿名取件功能不可用")
	}
	shareCode, err := s.lookupShareCode(ctx, pickupCode)
	if err != nil {
		return "", ErrCodeNotFound
	}
	fc, err := s.fileCodeRepo.GetByCode(ctx, shareCode)
	if err != nil || fc == nil {
		return "", ErrCodeNotFound
	}
	if fc.IsTextShare() || fc.GetFilePath() != "" {
		return "", errors.New("该取件码已完成上传，不能重复绑定")
	}
	if fc.IsExpired() {
		return "", ErrCodeExpired
	}
	return shareCode, nil
}

// BindFilePath 把直传完成的对象回填到占位分享（匿名取件码直传通道的最后一公里）。
// 以实际落盘对象为准更新 file_path 与 size；完成标记由调用方（presign Complete）保证。
// 占位记录自带 UUIDFileName（generate 的展示名），不清掉会被 GetFilePath()
// 拼成 objectKey/文件名 的双重路径——直传对象 key 本身就是完整相对路径。
func (s *Service) BindFilePath(ctx context.Context, shareCode, filePath string, size int64) error {
	fc, err := s.fileCodeRepo.GetByCode(ctx, shareCode)
	if err != nil || fc == nil {
		return ErrCodeNotFound
	}
	return s.fileCodeRepo.UpdateColumns(ctx, fc.ID, map[string]interface{}{
		"file_path":      filePath,
		"uuid_file_name": "",
		"size":           size,
	})
}

// randomShareCode 8 位 file_code（crypto/rand，小写字母+数字）
func randomShareCode() string {
	const charset = "abcdefghijklmnopqrstuvwxyz0123456789"
	return uidgen.RandomString(charset, 8)
}
