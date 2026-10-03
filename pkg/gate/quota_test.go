package gate

import (
	"context"
	"sync"
	"testing"

	"github.com/filescodebox/core/conf"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// resetQuotaMem 重置进程内计数器（测试隔离）
func resetQuotaMem(t *testing.T) {
	t.Helper()
	SetQuotaRedis(nil)
	quota.mu.Lock()
	quota.mem = map[string]*quotaEntry{}
	quota.memDate = ""
	quota.mu.Unlock()
}

func withQuotaConf(t *testing.T, count, bytes int64) {
	t.Helper()
	withConf(t, &conf.AppConfiguration{Upload: conf.UploadConfig{
		OpenUpload:          true,
		AnonymousDailyCount: count,
		AnonymousDailyBytes: bytes,
	}})
	t.Cleanup(func() { resetQuotaMem(t) })
}

func TestCheckAnonymousQuota_Disabled(t *testing.T) {
	withQuotaConf(t, 0, 0)
	assert.NoError(t, CheckAnonymousQuota(context.Background(), "1.2.3.4", 1<<20))
}

func TestCheckAnonymousQuota_CountLimit(t *testing.T) {
	withQuotaConf(t, 3, 0)
	resetQuotaMem(t)
	ctx := context.Background()
	for i := 0; i < 3; i++ {
		require.NoError(t, CheckAnonymousQuota(ctx, "1.2.3.4", 10))
	}
	err := CheckAnonymousQuota(ctx, "1.2.3.4", 10)
	require.Error(t, err)
	ge, ok := err.(*GateError)
	require.True(t, ok)
	assert.Equal(t, 10014, ge.ErrCode())

	// 不同 IP 各自独立计数
	assert.NoError(t, CheckAnonymousQuota(ctx, "5.6.7.8", 10))
}

func TestCheckAnonymousQuota_BytesLimit(t *testing.T) {
	withQuotaConf(t, 0, 1000)
	resetQuotaMem(t)
	ctx := context.Background()
	require.NoError(t, CheckAnonymousQuota(ctx, "1.2.3.4", 600))
	require.NoError(t, CheckAnonymousQuota(ctx, "1.2.3.4", 400))
	err := CheckAnonymousQuota(ctx, "1.2.3.4", 1)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "流量已达上限")
}

func TestCheckAnonymousQuota_DateRollover(t *testing.T) {
	withQuotaConf(t, 1, 0)
	resetQuotaMem(t)
	ctx := context.Background()
	require.NoError(t, CheckAnonymousQuota(ctx, "1.2.3.4", 10))
	assert.Error(t, CheckAnonymousQuota(ctx, "1.2.3.4", 10))

	// 模拟跨天：手工推进内存计数器的日期
	quota.mu.Lock()
	quota.memDate = "20000101"
	quota.mu.Unlock()
	assert.NoError(t, CheckAnonymousQuota(ctx, "1.2.3.4", 10))
}

func TestDailyQuota_Concurrent(t *testing.T) {
	withQuotaConf(t, 100, 0)
	resetQuotaMem(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = CheckAnonymousQuota(ctx, "9.9.9.9", 1)
		}()
	}
	wg.Wait()
	quota.mu.Lock()
	defer quota.mu.Unlock()
	assert.Equal(t, int64(50), quota.mem["9.9.9.9"].count, "并发计数不丢")
}
