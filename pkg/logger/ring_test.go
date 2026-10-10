package logger

import (
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap/zapcore"
)

// withTestRing 隔离全局环（用例结束还原），返回直驱入口。
func withTestRing(t *testing.T) *ringBuffer {
	t.Helper()
	old := logRing
	logRing = &ringBuffer{}
	t.Cleanup(func() { logRing = old })
	return logRing
}

func entry(level, msg string) RingEntry {
	return RingEntry{Time: time.Now(), Level: level, Message: msg}
}

func TestRingBuffer_AppendOrderAndSeq(t *testing.T) {
	r := withTestRing(t)
	r.append(entry("info", "a"))
	r.append(entry("warn", "b"))

	got, total := RecentLogs("", 0, 0)
	require.Equal(t, 2, total)
	assert.Equal(t, "a", got[0].Message)
	assert.Equal(t, "b", got[1].Message)
	assert.Equal(t, uint64(1), got[0].Seq, "序号单调递增供前端做 ID")
	assert.Equal(t, uint64(2), got[1].Seq)
}

func TestRingBuffer_WrapAroundKeepsNewestInOrder(t *testing.T) {
	r := withTestRing(t)
	n := ringCapacity + 500
	for i := 0; i < n; i++ {
		r.append(entry("info", fmt.Sprintf("m%05d", i)))
	}

	got, total := RecentLogs("", 0, 0)
	require.Equal(t, ringCapacity, total, "容量有界")
	// 最旧的 500 条被覆盖，窗口内最旧 = m00500，最新 = 末条
	assert.Equal(t, fmt.Sprintf("m%05d", n-ringCapacity), got[0].Message)
	assert.Equal(t, fmt.Sprintf("m%05d", n-1), got[ringCapacity-1].Message)
	// 时间序不回退（绕回后读取顺序仍最旧在前）
	for i := 1; i < len(got); i++ {
		assert.Greater(t, got[i].Seq, got[i-1].Seq)
	}
}

func TestRecentLogs_LevelFilterAndPagination(t *testing.T) {
	r := withTestRing(t)
	r.append(entry("info", "i1"))
	r.append(entry("error", "e1"))
	r.append(entry("warn", "w1"))
	r.append(entry("error", "e2"))

	// 级别精确过滤（大小写不敏感）
	errs, total := RecentLogs("ERROR", 0, 0)
	require.Equal(t, 2, total)
	assert.Equal(t, "e1", errs[0].Message)
	assert.Equal(t, "e2", errs[1].Message)

	// 分页窗口 + total 恒为过滤后总数（不受窗口影响）
	page, total := RecentLogs("", 1, 2)
	require.Equal(t, 4, total)
	require.Len(t, page, 2)
	assert.Equal(t, "e1", page[0].Message)
	assert.Equal(t, "w1", page[1].Message)

	// offset 越界 → 空窗口但 total 如实
	page, total = RecentLogs("", 99, 10)
	assert.Empty(t, page)
	assert.Equal(t, 4, total)
}

func TestRingCore_LevelGate(t *testing.T) {
	withTestRing(t)
	c := newRingCore(zapcore.InfoLevel)
	assert.True(t, c.Enabled(zapcore.ErrorLevel))
	assert.False(t, c.Enabled(zapcore.DebugLevel))
	assert.NoError(t, c.Sync())
}
