package auth

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestRefreshToken_Blacklist 登出（黑名单）后的 token 不得再刷新出新 token。
// 此前 /api/v1/user/refresh 不校验黑名单，登出撤销可被绕过（设计文档 §9.6）。
func TestRefreshToken_Blacklist(t *testing.T) {
	SetJWTSecret("test-secret-for-refresh")
	t.Cleanup(func() { SetJWTSecret("") })

	ctx := context.Background()
	token, err := GenerateToken(7, "alice", "user")
	require.NoError(t, err)

	// 未吊销：可正常刷新
	newToken, err := RefreshToken(ctx, token)
	require.NoError(t, err)
	assert.NotEmpty(t, newToken)

	// 吊销后：刷新必须被拒（ErrTokenRevoked）
	RevokeToken(ctx, token, time.Minute)
	_, err = RefreshToken(ctx, token)
	require.Error(t, err)
	assert.True(t, errors.Is(err, ErrTokenRevoked), "期望 ErrTokenRevoked，实际: %v", err)

	// 新 token 不受旧 token 吊销影响
	_, err = ParseToken(newToken)
	require.NoError(t, err)
}

func TestRefreshToken_Invalid(t *testing.T) {
	SetJWTSecret("test-secret-for-refresh")
	t.Cleanup(func() { SetJWTSecret("") })

	_, err := RefreshToken(context.Background(), "not.a.jwt")
	require.Error(t, err)
}
