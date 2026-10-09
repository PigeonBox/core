package redisshim

import (
	"context"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKV_GetSetDel(t *testing.T) {
	kv := New()
	ctx := context.Background()

	// 未写入 → redis.Nil（与 go-redis 未命中语义一致）
	err := kv.Get(ctx, "missing").Err()
	assert.ErrorIs(t, err, redis.Nil)

	require.NoError(t, kv.Set(ctx, "k", "v", time.Minute).Err())
	got, err := kv.Get(ctx, "k").Result()
	require.NoError(t, err)
	assert.Equal(t, "v", got)

	// []byte 值归一化为 string
	require.NoError(t, kv.Set(ctx, "b", []byte("raw"), time.Minute).Err())
	got, err = kv.Get(ctx, "b").Result()
	require.NoError(t, err)
	assert.Equal(t, "raw", got)

	cnt, err := kv.Del(ctx, "k", "b", "never-existed").Result()
	require.NoError(t, err)
	assert.Equal(t, int64(2), cnt, "存在键计 2，不存在键不计")
	cnt, err = kv.Del(ctx, "k").Result()
	require.NoError(t, err)
	assert.Equal(t, int64(0), cnt, "已删键再删计数 0")
}

func TestKV_SetArgs_NX(t *testing.T) {
	kv := New()
	ctx := context.Background()

	// NX 首次命中
	cmd := kv.SetArgs(ctx, "code", "1", redis.SetArgs{Mode: "NX", TTL: time.Minute})
	require.NoError(t, cmd.Err())

	// NX 二次未命中 → redis.Nil（服务层按此重试换码）
	cmd = kv.SetArgs(ctx, "code", "2", redis.SetArgs{Mode: "NX", TTL: time.Minute})
	assert.ErrorIs(t, cmd.Err(), redis.Nil)
	v, err := kv.Get(ctx, "code").Result()
	require.NoError(t, err)
	assert.Equal(t, "1", v, "NX 未命中不得覆写")

	// 空 Mode = 普通 Set
	require.NoError(t, kv.SetArgs(ctx, "code", "3", redis.SetArgs{TTL: time.Minute}).Err())
	v, err = kv.Get(ctx, "code").Result()
	require.NoError(t, err)
	assert.Equal(t, "3", v)

	// 其余 Mode fail-loud
	cmd = kv.SetArgs(ctx, "x", "1", redis.SetArgs{Mode: "XX"})
	assert.Error(t, cmd.Err())
}

func TestKV_TTLExpiry(t *testing.T) {
	kv := New()
	ctx := context.Background()
	require.NoError(t, kv.Set(ctx, "ephemeral", "v", 10*time.Millisecond).Err())
	time.Sleep(30 * time.Millisecond)
	assert.ErrorIs(t, kv.Get(ctx, "ephemeral").Err(), redis.Nil)
}

func TestKV_IndependentInstances(t *testing.T) {
	a, b := New(), New()
	ctx := context.Background()
	require.NoError(t, a.Set(ctx, "k", "v", time.Minute).Err())
	assert.ErrorIs(t, b.Get(ctx, "k").Err(), redis.Nil, "New 各自持独立表")
}
