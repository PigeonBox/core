package anonymous

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/pigeonbox/core/repo/db/model"
)

// TestMintForShare 文件分享铸造取件码（2026-10-07 对标"文件另有 6 位取件码"语义）。
// 铸造只写 KV 映射，不动 DB；永久分享/过期时间不铸造。
func TestMintForShare(t *testing.T) {
	svc, _, _ := newTestService(t)
	ctx := context.Background()
	expireAt := time.Now().Add(time.Hour)

	// Peek/Retrieve 的 DB 回源需要分享记录真实存在（生产语义：分享先于取件码）
	seed := func(code string, requireAuth bool) {
		require.NoError(t, svc.fileCodeRepo.Create(ctx, &model.FileCode{
			Code:         code,
			FilePath:     "uploads/x/" + code + ".bin",
			ExpiredCount: -1, // -1=时间式无限次；Go 零值 0 语义是"次数耗尽即过期"
			RequireAuth:  requireAuth,
		}))
	}
	seed("shareabc", false)
	seed("sharepwd", true)

	t.Run("正常铸造并可解析", func(t *testing.T) {
		pickup, err := svc.MintForShare(ctx, "shareabc", "demo.txt", 128, false, &expireAt)
		require.NoError(t, err)
		require.Len(t, pickup, codeLength)

		// Peek 经映射解析回分享码（与 Retrieve 同一 lookup 路径）
		meta, fc, err := svc.Peek(ctx, pickup)
		require.NoError(t, err)
		assert.Equal(t, "shareabc", fc.Code)
		assert.Equal(t, "shareabc", meta.ShareCode)
		assert.Equal(t, "demo.txt", meta.FileName)
		assert.Equal(t, int64(128), meta.FileSize)
	})

	t.Run("永久分享不铸造", func(t *testing.T) {
		pickup, err := svc.MintForShare(ctx, "shareperm", "p.bin", 1, false, nil)
		require.NoError(t, err)
		assert.Empty(t, pickup, "永久分享 KV TTL 无法对齐，应返回空串跳过")
	})

	t.Run("过期时间已过不铸造", func(t *testing.T) {
		past := time.Now().Add(-time.Minute)
		pickup, err := svc.MintForShare(ctx, "shareold", "o.bin", 1, false, &past)
		require.NoError(t, err)
		assert.Empty(t, pickup)
	})

	t.Run("密码保护标记进展示信息", func(t *testing.T) {
		pickup, err := svc.MintForShare(ctx, "sharepwd", "s.bin", 1, true, &expireAt)
		require.NoError(t, err)
		require.Len(t, pickup, codeLength)
		_, fc, err := svc.Peek(ctx, pickup)
		require.NoError(t, err)
		assert.True(t, fc.RequireAuth)
	})
}
