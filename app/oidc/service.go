// Package oidc 实现 OIDC 单点登录（P2，stdlib-only，无第三方依赖）。
//
// 流程（Authorization Code）：
//  1. GET /api/v1/user/oidc/login → 302 到 IdP 授权页（state 为 HMAC 签名时间戳，防 CSRF）
//  2. IdP 回调 GET /api/v1/user/oidc/callback?code&state → 验 state → 换 token → 拉 userinfo
//  3. 按 (issuer, sub) 匹配本地用户，其次按 email，未命中且允许注册则建号
//  4. 签发本站 JWT，302 回前端 /#/oidc/callback?token=xxx
package oidc

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"gorm.io/gorm"

	"github.com/filescodebox/core/pkg/auth"
	"github.com/filescodebox/core/repo/db/dao"
	"github.com/filescodebox/core/repo/db/model"
)

// Config OIDC 配置（conf.SecurityConfig.OIDC）
type Config struct {
	Enabled      bool   `mapstructure:"enabled"`
	Issuer       string `mapstructure:"issuer"`        // 如 https://idp.example.com/realms/fcb
	ClientID     string `mapstructure:"client_id"`
	ClientSecret string `mapstructure:"client_secret"`
	Scopes       string `mapstructure:"scopes"`        // 默认 "openid profile email"
	FrontendCallback string `mapstructure:"frontend_callback"` // 默认 /#/oidc/callback
}

// discovery 端点发现缓存
type discovery struct {
	AuthorizationEndpoint string `json:"authorization_endpoint"`
	TokenEndpoint         string `json:"token_endpoint"`
	UserinfoEndpoint      string `json:"userinfo_endpoint"`
}

// Service OIDC 登录服务
type Service struct {
	cfg    Config
	client *http.Client

	mu      sync.RWMutex
	disc    *discovery
	discAt  time.Time

	userRepo *dao.UserRepository
}

// NewService 构建服务
func NewService(cfg Config) *Service {
	if cfg.Scopes == "" {
		cfg.Scopes = "openid profile email"
	}
	if cfg.FrontendCallback == "" {
		cfg.FrontendCallback = "/#/oidc/callback"
	}
	return &Service{
		cfg:      cfg,
		client:   &http.Client{Timeout: 15 * time.Second},
		userRepo: dao.NewUserRepository(),
	}
}

// Enabled 是否启用
func (s *Service) Enabled() bool { return s.cfg.Enabled && s.cfg.Issuer != "" && s.cfg.ClientID != "" }

// RedirectURI 回调地址（本站）
func (s *Service) RedirectURI(baseURL string) string {
	return strings.TrimSuffix(baseURL, "/") + "/api/v1/user/oidc/callback"
}

// FrontendCallback 前端回调地址（带 token= 前缀；hash 路由 query）
func (s *Service) FrontendCallback() string {
	sep := "?"
	if strings.Contains(s.cfg.FrontendCallback, "?") {
		sep = "&"
	}
	return s.cfg.FrontendCallback + sep + "token="
}

// discover 发现端点（缓存 1 小时）
func (s *Service) discover(ctx context.Context) (*discovery, error) {
	s.mu.RLock()
	if s.disc != nil && time.Since(s.discAt) < time.Hour {
		d := s.disc
		s.mu.RUnlock()
		return d, nil
	}
	s.mu.RUnlock()

	issuer := strings.TrimSuffix(s.cfg.Issuer, "/")
	wellKnown := issuer + "/.well-known/openid-configuration"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, wellKnown, nil)
	if err != nil {
		return nil, err
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("OIDC discovery 请求失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("OIDC discovery 返回 %d", resp.StatusCode)
	}
	var d discovery
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&d); err != nil {
		return nil, fmt.Errorf("OIDC discovery 解析失败: %w", err)
	}
	if d.AuthorizationEndpoint == "" || d.TokenEndpoint == "" {
		return nil, errors.New("OIDC discovery 缺少必要端点")
	}
	s.mu.Lock()
	s.disc = &d
	s.discAt = time.Now()
	s.mu.Unlock()
	return &d, nil
}

// signState HMAC 签名时间戳（1 小时有效；无需服务端会话）
func (s *Service) signState(ts int64) string {
	mac := hmac.New(sha256.New, []byte(s.cfg.ClientSecret))
	fmt.Fprintf(mac, "oidc-state:%d", ts)
	return fmt.Sprintf("%d.%s", ts, hex.EncodeToString(mac.Sum(nil)))
}

func (s *Service) verifyState(state string) bool {
	var ts int64
	var sig string
	if _, err := fmt.Sscanf(state, "%d.%s", &ts, &sig); err != nil {
		return false
	}
	if time.Since(time.Unix(ts, 0)) > time.Hour {
		return false
	}
	return hmac.Equal([]byte(s.signState(ts)), []byte(state))
}

// LoginURL 构建授权跳转地址
func (s *Service) LoginURL(ctx context.Context, baseURL string) (string, error) {
	d, err := s.discover(ctx)
	if err != nil {
		return "", err
	}
	ts := time.Now().Unix()
	q := url.Values{}
	q.Set("response_type", "code")
	q.Set("client_id", s.cfg.ClientID)
	q.Set("redirect_uri", s.RedirectURI(baseURL))
	q.Set("scope", s.cfg.Scopes)
	q.Set("state", s.signState(ts))
	// PKCE 可选：部分 IdP 对公共客户端要求；机密客户端可省略（v1 不启用）
	return d.AuthorizationEndpoint + "?" + q.Encode(), nil
}

// idClaims userinfo 载荷
type idClaims struct {
	Sub               string `json:"sub"`
	Email             string `json:"email"`
	EmailVerified     bool   `json:"email_verified"`
	PreferredUsername string `json:"preferred_username"`
	Name              string `json:"name"`
}

// ExchangeCallback 处理回调：code → token → userinfo → 本地用户 → JWT
func (s *Service) ExchangeCallback(ctx context.Context, baseURL, code, state string) (string, error) {
	if !s.verifyState(state) {
		return "", errors.New("state 校验失败（过期或伪造），请重试登录")
	}
	d, err := s.discover(ctx)
	if err != nil {
		return "", err
	}

	// 1. 换 token
	form := url.Values{}
	form.Set("grant_type", "authorization_code")
	form.Set("code", code)
	form.Set("redirect_uri", s.RedirectURI(baseURL))
	form.Set("client_id", s.cfg.ClientID)
	form.Set("client_secret", s.cfg.ClientSecret)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.TokenEndpoint, strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	resp, err := s.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("token 交换失败: %w", err)
	}
	defer resp.Body.Close()
	var tok struct {
		AccessToken string `json:"access_token"`
		Error       string `json:"error"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&tok); err != nil || tok.AccessToken == "" {
		return "", fmt.Errorf("token 响应无效: %v %s", err, tok.Error)
	}

	// 2. 拉 userinfo
	ureq, err := http.NewRequestWithContext(ctx, http.MethodGet, d.UserinfoEndpoint, nil)
	if err != nil {
		return "", err
	}
	ureq.Header.Set("Authorization", "Bearer "+tok.AccessToken)
	uresp, err := s.client.Do(ureq)
	if err != nil {
		return "", fmt.Errorf("userinfo 请求失败: %w", err)
	}
	defer uresp.Body.Close()
	if uresp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("userinfo 返回 %d", uresp.StatusCode)
	}
	var claims idClaims
	if err := json.NewDecoder(io.LimitReader(uresp.Body, 1<<20)).Decode(&claims); err != nil {
		return "", fmt.Errorf("userinfo 解析失败: %w", err)
	}
	if claims.Sub == "" {
		return "", errors.New("userinfo 缺少 sub")
	}

	// 3. 匹配/创建本地用户
	user, err := s.matchOrCreate(ctx, claims)
	if err != nil {
		return "", err
	}

	// 4. 签发本站 JWT
	return auth.GenerateToken(user.ID, user.Username, user.Role)
}

// matchOrCreate (issuer,sub) → email → 建号
func (s *Service) matchOrCreate(ctx context.Context, claims idClaims) (*model.User, error) {
	// 按 sub 精确匹配
	if u, err := s.userRepo.GetByOIDCSub(ctx, claims.Sub); err == nil && u != nil {
		return u, nil
	}
	// 按邮箱匹配（用户先以密码注册、后用 OIDC 登录的场景）
	if claims.Email != "" {
		if u, err := s.userRepo.GetByEmail(ctx, claims.Email); err == nil && u != nil {
			// 绑定 sub，后续走精确匹配
			_ = s.userRepo.UpdateColumns(ctx, u.ID, map[string]interface{}{"oidc_sub": claims.Sub})
			return u, nil
		}
	}
	// 建号（用户名去重）
	username := claims.PreferredUsername
	if username == "" {
		username = claims.Name
	}
	if username == "" && claims.Email != "" {
		username = strings.SplitN(claims.Email, "@", 2)[0]
	}
	if username == "" {
		username = "oidc-" + claims.Sub[:8]
	}
	base := username
	for i := 1; ; i++ {
		if _, err := s.userRepo.GetByUsername(ctx, username); err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				break
			}
			return nil, err
		}
		username = fmt.Sprintf("%s-%d", base, i)
	}
	// 随机密码（OIDC 用户不可走密码登录）
	pw := make([]byte, 24)
	_, _ = rand.Read(pw)
	user := &model.User{
		Username:    username,
		Email:       claims.Email,
		Nickname:    claims.Name,
		Role:        "user",
		Status:      "active",
		OidcSub:     claims.Sub,
	}
	if claims.Email == "" {
		// email 唯一索引不接受空串重复：以 sub 生成占位
		user.Email = fmt.Sprintf("oidc-%s@oidc.local", claims.Sub[:8])
	}
	if err := s.userRepo.Create(ctx, user); err != nil {
		return nil, fmt.Errorf("OIDC 用户创建失败: %w", err)
	}
	return user, nil
}
