// auth.go 认证职责簇：管理员登录凭据校验与 JWT 签发（GenerateTokenForAdmin 属认证职责，单独成文件）。
package admin

import (
	"context"
	"errors"
	"time"

	"github.com/pigeonbox/core/pkg/auth"
	"golang.org/x/crypto/bcrypt"
)

// GenerateTokenForAdmin 生成管理员登录 token
func (s *Service) GenerateTokenForAdmin(ctx context.Context, username, password string) (string, error) {
	// 查找用户
	user, err := s.userRepo.GetByUsername(ctx, username)
	if err != nil {
		return "", errors.New("用户名或密码错误")
	}

	// 验证密码（2026-10-05 审计 P3：先比密码再判角色——原顺序下错误信息
	// "权限不足" vs "用户名或密码错误" 可区分"该用户名是管理员"，构成
	// 管理员用户名探测预言机）
	if err := bcrypt.CompareHashAndPassword([]byte(user.PasswordHash), []byte(password)); err != nil {
		return "", errors.New("用户名或密码错误")
	}

	// 检查是否为管理员
	if user.Role != "admin" {
		return "", errors.New("权限不足")
	}

	// 检查用户状态
	if user.Status != "active" {
		return "", errors.New("用户已被禁用")
	}

	// 生成 token (24小时过期)
	token, err := auth.GenerateToken(user.ID, user.Username, "admin")
	if err != nil {
		return "", errors.New("生成 token 失败")
	}

	// 更新最后登录时间
	now := time.Now()
	user.LastLoginAt = &now
	_ = s.userRepo.Update(ctx, user)

	return token, nil
}
