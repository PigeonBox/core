// Package notify 实现系统通知（管理员公告 + per-user 取件通知）service。
//
// 分层：通过 dao.NotifyRepository 访问数据（与其他 service 一致），
// 不再直接持有 *gorm.DB。
package notify

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/filescodebox/core/pkg/logger"
	"github.com/filescodebox/core/repo/db/dao"
	"github.com/filescodebox/core/repo/db/model"
	"github.com/filescodebox/kit/async"
	"github.com/filescodebox/kit/httpjson"
)

// webhookHTTPClient webhook 推送专用客户端（5s 超时；推送失败静默记日志不重试）。
var webhookHTTPClient = &http.Client{Timeout: 5 * time.Second}

// 错误
var (
	ErrNotifyNotFound = errors.New("notify not found")
	ErrInvalidParam   = errors.New("invalid parameter")
)

// Service 通知 service
type Service struct {
	notifyRepo *dao.NotifyRepository
	webhookURL string      // 外部 Webhook 推送地址（空 = 禁用）
	mailer     *SMTPMailer // 邮件通知渠道（nil = 禁用；P2 SMTP）
}

// NewService 创建 service（内部自建 repo，走全局 db.GetDB()）
func NewService() *Service {
	return &Service{notifyRepo: dao.NewNotifyRepository()}
}

// ListItem 列表项
type ListItem struct {
	ID        uint       `json:"id"`
	Title     string     `json:"title"`
	Content   string     `json:"content"`
	Type      string     `json:"type"`
	Level     string     `json:"level"`
	Status    int        `json:"status"`
	StartAt   *time.Time `json:"start_at,omitempty"`
	EndAt     *time.Time `json:"end_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

// ListData 列表数据
type ListData struct {
	Items    []ListItem `json:"items"`
	Total    int64      `json:"total"`
	Page     int        `json:"page"`
	PageSize int        `json:"page_size"`
}

// ListReq 列表请求
type ListReq struct {
	Page     int
	PageSize int
	Type     string
	Level    string
	Status   *int
}

// List 列表
func (s *Service) List(ctx context.Context, req ListReq) (*ListData, error) {
	if req.Page <= 0 {
		req.Page = 1
	}
	if req.PageSize <= 0 || req.PageSize > 100 {
		req.PageSize = 20
	}
	tx := s.notifyRepo.Query(ctx)
	if req.Type != "" {
		tx = tx.Where("type = ?", req.Type)
	}
	if req.Level != "" {
		tx = tx.Where("level = ?", req.Level)
	}
	if req.Status != nil {
		tx = tx.Where("status = ?", *req.Status)
	}
	var total int64
	if err := tx.Count(&total).Error; err != nil {
		return nil, err
	}
	var items []model.Notify
	if err := tx.Order("created_at DESC").Offset((req.Page - 1) * req.PageSize).Limit(req.PageSize).Find(&items).Error; err != nil {
		return nil, err
	}
	result := &ListData{
		Items:    make([]ListItem, 0, len(items)),
		Total:    total,
		Page:     req.Page,
		PageSize: req.PageSize,
	}
	for _, n := range items {
		result.Items = append(result.Items, toListItem(&n))
	}
	return result, nil
}

// Get 单条
func (s *Service) Get(ctx context.Context, id uint) (*ListItem, error) {
	n, err := s.notifyRepo.GetByID(ctx, id)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return nil, ErrNotifyNotFound
		}
		return nil, err
	}
	item := toListItem(n)
	return &item, nil
}

// Active 当前活跃通知（公开 API，供全站横幅）。
// 仅广播（target_user_id 空/0）：定向站内信绝不能从无认证端点泄露，
// 否则取件码/投递详情会广播给匿名访客；定向通知走 /api/v1/notifies/mine。
func (s *Service) Active(ctx context.Context, typ string) ([]ListItem, error) {
	tx := s.notifyRepo.Query(ctx).
		Where("status = ?", 1).
		Where("target_user_id IS NULL OR target_user_id = ?", 0).
		Where("start_at IS NULL OR start_at <= ?", time.Now()).
		Where("end_at IS NULL OR end_at >= ?", time.Now())
	if typ != "" {
		tx = tx.Where("type = ?", typ)
	}
	var items []model.Notify
	if err := tx.Order("created_at DESC").Limit(50).Find(&items).Error; err != nil {
		return nil, err
	}
	result := make([]ListItem, 0, len(items))
	for i := range items {
		result = append(result, toListItem(&items[i]))
	}
	return result, nil
}

// CreateReq 创建请求
type CreateReq struct {
	Title    string
	Content  string
	Type     string
	Level    string
	Status   int
	StartAt  *time.Time
	EndAt    *time.Time
	AuthorID uint
}

// Create 创建
func (s *Service) Create(ctx context.Context, req CreateReq) (*ListItem, error) {
	if req.Title == "" || req.Content == "" {
		return nil, ErrInvalidParam
	}
	if req.Type == "" {
		req.Type = "system"
	}
	if req.Level == "" {
		req.Level = "info"
	}
	if req.Status == 0 {
		req.Status = 1
	}
	n := model.Notify{
		Title:    req.Title,
		Content:  req.Content,
		Type:     req.Type,
		Level:    req.Level,
		Status:   req.Status,
		StartAt:  req.StartAt,
		EndAt:    req.EndAt,
		AuthorID: req.AuthorID,
	}
	if err := s.notifyRepo.Create(ctx, &n); err != nil {
		return nil, err
	}
	item := toListItem(&n)
	return &item, nil
}

// UpdateReq 更新请求
type UpdateReq struct {
	ID      uint
	Title   *string
	Content *string
	Type    *string
	Level   *string
	Status  *int
	StartAt *time.Time
	EndAt   *time.Time
}

// Update 更新
func (s *Service) Update(ctx context.Context, req UpdateReq) error {
	updates := map[string]interface{}{}
	if req.Title != nil {
		updates["title"] = *req.Title
	}
	if req.Content != nil {
		updates["content"] = *req.Content
	}
	if req.Type != nil {
		updates["type"] = *req.Type
	}
	if req.Level != nil {
		updates["level"] = *req.Level
	}
	if req.Status != nil {
		updates["status"] = *req.Status
	}
	if req.StartAt != nil {
		updates["start_at"] = *req.StartAt
	}
	if req.EndAt != nil {
		updates["end_at"] = *req.EndAt
	}
	if len(updates) == 0 {
		return nil
	}
	affected, err := s.notifyRepo.UpdateByID(ctx, req.ID, updates)
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotifyNotFound
	}
	return nil
}

// Delete 删除
func (s *Service) Delete(ctx context.Context, id uint) error {
	affected, err := s.notifyRepo.DeleteByID(ctx, id)
	if err != nil {
		return err
	}
	if affected == 0 {
		return ErrNotifyNotFound
	}
	return nil
}

// ============ 辅助 ============

func toListItem(n *model.Notify) ListItem {
	return ListItem{
		ID:        n.ID,
		Title:     n.Title,
		Content:   n.Content,
		Type:      n.Type,
		Level:     n.Level,
		Status:    n.Status,
		StartAt:   n.StartAt,
		EndAt:     n.EndAt,
		CreatedAt: n.CreatedAt,
		UpdatedAt: n.UpdatedAt,
	}
}

// ParseID 解析 path 中的 id
func ParseID(s string) (uint, error) {
	id, err := strconv.ParseUint(s, 10, 64)
	if err != nil {
		return 0, ErrInvalidParam
	}
	return uint(id), nil
}

// ============ Per-user 通知（A2 取件通知） ============

// UserNotifyItem 用户通知项
type UserNotifyItem struct {
	ID        uint       `json:"id"`
	Title     string     `json:"title"`
	Content   string     `json:"content"`
	Type      string     `json:"type"`
	Level     string     `json:"level"`
	ReadAt    *time.Time `json:"read_at,omitempty"`
	CreatedAt time.Time  `json:"created_at"`
	IsRead    bool       `json:"is_read"`
}

// UserNotifyListData 用户通知列表
type UserNotifyListData struct {
	Items      []UserNotifyItem `json:"items"`
	Total      int64            `json:"total"`
	Unread     int64            `json:"unread"`
	Page       int              `json:"page"`
	PageSize   int              `json:"page_size"`
	TotalPages int64            `json:"total_pages"`
}

// ListForUser 列出某用户的通知（含广播 + 定向给该用户）
func (s *Service) ListForUser(ctx context.Context, userID uint, page, pageSize int) (*UserNotifyListData, error) {
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	q := s.notifyRepo.Query(ctx).
		Where("status = 1").
		Where("target_user_id IS NULL OR target_user_id = ?", userID)

	var total int64
	if err := q.Count(&total).Error; err != nil {
		return nil, err
	}
	unread, err := s.notifyRepo.CountWhere(ctx,
		"status = 1 AND (target_user_id IS NULL OR target_user_id = ?) AND read_at IS NULL",
		[]interface{}{userID})
	if err != nil {
		return nil, err
	}

	var rows []model.Notify
	if err := q.Order("created_at DESC").
		Offset((page - 1) * pageSize).Limit(pageSize).
		Find(&rows).Error; err != nil {
		return nil, err
	}

	items := make([]UserNotifyItem, 0, len(rows))
	for i := range rows {
		n := &rows[i]
		items = append(items, UserNotifyItem{
			ID:        n.ID,
			Title:     n.Title,
			Content:   n.Content,
			Type:      n.Type,
			Level:     n.Level,
			ReadAt:    n.ReadAt,
			CreatedAt: n.CreatedAt,
			IsRead:    n.ReadAt != nil,
		})
	}

	totalPages := (total + int64(pageSize) - 1) / int64(pageSize)
	if totalPages < 1 {
		totalPages = 1
	}
	return &UserNotifyListData{
		Items: items, Total: total, Unread: unread,
		Page: page, PageSize: pageSize, TotalPages: totalPages,
	}, nil
}

// UnreadCountForUser 某用户未读数
func (s *Service) UnreadCountForUser(ctx context.Context, userID uint) (int64, error) {
	return s.notifyRepo.CountWhere(ctx,
		"status = 1 AND (target_user_id IS NULL OR target_user_id = ?) AND read_at IS NULL",
		[]interface{}{userID})
}

// MarkAllReadForUser 标记某用户所有通知为已读（只更新 read_at IS NULL 的）
func (s *Service) MarkAllReadForUser(ctx context.Context, userID uint) (int64, error) {
	return s.notifyRepo.UpdateWhere(ctx,
		"status = 1 AND (target_user_id IS NULL OR target_user_id = ?) AND read_at IS NULL",
		[]interface{}{userID}, "read_at", time.Now())
}

// CreateForUser 创建一条定向通知（owner 取件通知用）
func (s *Service) CreateForUser(ctx context.Context, userID uint, title, content, notifyType, level string) (*model.Notify, error) {
	uid := userID
	n := &model.Notify{
		Title:        title,
		Content:      content,
		Type:         notifyType,
		Level:        level,
		Status:       1,
		AuthorID:     0,
		TargetUserID: &uid,
	}
	if err := s.notifyRepo.Create(ctx, n); err != nil {
		return nil, err
	}
	return n, nil
}

// CreateForUserSimple 简化版（返回 error，匹配 share.NotifyServiceInterface）
func (s *Service) CreateForUserSimple(ctx context.Context, userID uint, title, content, notifyType, level string) error {
	_, err := s.CreateForUser(ctx, userID, title, content, notifyType, level)
	// 外部 Webhook 推送（fire-and-forget；对标上游缺口，双方都缺的外部通知渠道）
	if err == nil {
		s.dispatchWebhook(ctx, userID, title, content, notifyType, level)
		// 邮件补发（fire-and-forget；未配置 SMTP/用户无邮箱时内部短路）
		if s.mailer != nil && s.mailer.Enabled() {
			go s.mailer.SendToUser(userID, title, content)
		}
	}
	return err
}

// SetMailer 注入邮件发送器（bootstrap 从 notify.smtp 段构建；nil = 禁用）
func (s *Service) SetMailer(m *SMTPMailer) { s.mailer = m }

// ==================== Webhook 外部通知渠道 ====================

// SetWebhookURL 注入 Webhook 推送地址（bootstrap 从 notify.webhook.url / FCB_WEBHOOK_URL 注入；
// 空串 = 禁用）。推送失败只记日志，绝不影响站内信主流程。
func (s *Service) SetWebhookURL(url string) {
	s.webhookURL = strings.TrimSpace(url)
}

// webhookPayload Webhook POST 的 JSON 载荷
type webhookPayload struct {
	Event      string `json:"event"`
	Title      string `json:"title"`
	Content    string `json:"content"`
	NotifyType string `json:"notify_type"`
	Level      string `json:"level"`
	UserID     uint   `json:"user_id"`
	Timestamp  int64  `json:"timestamp"`
}

// EmitShareFlagged 分享命中审核钩子事件（share.flagged）——结构化 webhook 推送，
// 供外挂自动处置（如拉取后调管理端禁用接口）。异步推送，失败静默记日志。
func (s *Service) EmitShareFlagged(code, reason, ownerIP string) {
	if s.webhookURL == "" {
		return
	}
	payload := map[string]interface{}{
		"event":    "share.flagged",
		"code":     code,
		"reason":   reason,
		"owner_ip": ownerIP,
		"hint":     "share is set pending_review; approve via PUT /admin/files/:id/status",
		"timestamp": time.Now().Unix(),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	event := "share.flagged"
	async.GoSafe(func() {
		err := httpjson.DoJSON(context.Background(), webhookHTTPClient, httpjson.Request{
			Method: http.MethodPost,
			URL:    s.webhookURL,
			Body:   body,
			Header: func(h http.Header) {
				h.Set("Content-Type", "application/json")
				h.Set("X-FCB-Event", event)
			},
		}, nil)
		if err != nil {
			logger.Warn("webhook push failed", zap.String("url", s.webhookURL), zap.Error(err))
		}
	})
}

// dispatchWebhook 异步推送（5s 超时，失败静默记日志）
func (s *Service) dispatchWebhook(ctx context.Context, userID uint, title, content, notifyType, level string) {
	if s.webhookURL == "" {
		return
	}
	payload := webhookPayload{
		Event:      "notify.created",
		Title:      title,
		Content:    content,
		NotifyType: notifyType,
		Level:      level,
		UserID:     userID,
		Timestamp:  time.Now().Unix(),
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	async.GoSafe(func() {
		err := httpjson.DoJSON(ctx, webhookHTTPClient, httpjson.Request{
			Method: http.MethodPost,
			URL:    s.webhookURL,
			Body:   body,
			Header: func(h http.Header) {
				h.Set("Content-Type", "application/json")
				h.Set("X-FCB-Event", "notify.created")
			},
		}, nil)
		if err != nil {
			logger.Warn("webhook push failed", zap.String("url", s.webhookURL), zap.Error(err))
		}
	})
}
