package memkv

import (
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGetSet_Basic 覆盖写入/读取/覆盖语义。
func TestGetSet_Basic(t *testing.T) {
	s := New()
	_, ok := s.Get("k")
	assert.False(t, ok, "空表不应命中")

	s.Set("k", "v1", time.Minute)
	v, ok := s.Get("k")
	require.True(t, ok)
	assert.Equal(t, "v1", v)

	s.Set("k", "v2", time.Minute) // 覆盖
	v, ok = s.Get("k")
	require.True(t, ok)
	assert.Equal(t, "v2", v)
	assert.Equal(t, 1, s.Len())
}

// TestGet_TTLExpiry 时钟注入验证过期（惰性删除）。
func TestGet_TTLExpiry(t *testing.T) {
	now := time.Now()
	s := New().WithClock(func() time.Time { return now })

	s.Set("k", "v", time.Second)
	_, ok := s.Get("k")
	require.True(t, ok)

	now = now.Add(2 * time.Second)
	_, ok = s.Get("k")
	assert.False(t, ok, "过期后应未命中")
	assert.Equal(t, 0, s.Len(), "过期条目应被惰性删除")
}

// TestSetNX_Atomic 不存在才写入；过期条目可被 SetNX 覆写。
func TestSetNX_Atomic(t *testing.T) {
	now := time.Now()
	s := New().WithClock(func() time.Time { return now })

	assert.True(t, s.SetNX("k", "v1", time.Minute), "首次写入成功")
	assert.False(t, s.SetNX("k", "v2", time.Minute), "已存在拒绝")
	v, _ := s.Get("k")
	assert.Equal(t, "v1", v, "拒绝时不得覆盖")

	now = now.Add(2 * time.Minute)
	assert.True(t, s.SetNX("k", "v3", time.Minute), "过期条目视为不存在")
	v, _ = s.Get("k")
	assert.Equal(t, "v3", v)
}

// TestSetNX_Concurrent 并发 SetNX 同键仅一人成功（NX 唯一性语义）。
func TestSetNX_Concurrent(t *testing.T) {
	s := New()
	const n = 50
	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		winners  int
		loserVal string
	)
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if s.SetNX("race", "w"+string(rune('a'+i)), time.Minute) {
				mu.Lock()
				winners++
				loserVal = "w" + string(rune('a'+i))
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	assert.Equal(t, 1, winners, "并发 NX 只允许一个写入者")
	v, _ := s.Get("race")
	assert.Equal(t, loserVal, v)
}

// TestDel 返回实际删除数，未命中计 0。
func TestDel(t *testing.T) {
	s := New()
	s.Set("a", "1", time.Minute)
	s.Set("b", "2", time.Minute)
	assert.Equal(t, 2, s.Del("a", "b", "c"))
	assert.Equal(t, 0, s.Len())
	assert.Equal(t, 0, s.Del("a"))
}

// TestSweep_AndCapacityReset 满载守卫：满载时插入新键先清扫过期条目让位；
// 无过期可清时整表重置（保命优先）。
func TestSweep_AndCapacityReset(t *testing.T) {
	now := time.Now()
	s := New().WithClock(func() time.Time { return now })

	// 满载且全部已过期：插入新键前清扫让位，不触发重置
	for i := 0; i < DefaultMax; i++ {
		s.Set("old"+strconv.Itoa(i), "v", time.Second)
	}
	require.Equal(t, DefaultMax, s.Len())
	now = now.Add(2 * time.Second)
	s.Set("fresh", "v", time.Minute) // 触发满载清扫
	assert.Equal(t, 1, s.Len(), "过期条目应被清扫，仅剩新键")
	v, ok := s.Get("fresh")
	require.True(t, ok)
	assert.Equal(t, "v", v)

	// 满载且全部未过期：整表重置
	s.Del("fresh")
	for i := 0; i < DefaultMax; i++ {
		s.Set("k"+strconv.Itoa(i), "v", time.Hour)
	}
	require.Equal(t, DefaultMax, s.Len())
	s.Set("overflow", "v", time.Minute)
	assert.Equal(t, 1, s.Len(), "仍满应整表重置")
	v, ok = s.Get("overflow")
	require.True(t, ok)
	assert.Equal(t, "v", v)
}

// TestZeroTTL_NoExpiry ttl<=0 = 永不过期。
func TestZeroTTL_NoExpiry(t *testing.T) {
	now := time.Now()
	s := New().WithClock(func() time.Time { return now })
	s.Set("k", "v", 0)
	now = now.Add(100 * time.Hour)
	v, ok := s.Get("k")
	require.True(t, ok)
	assert.Equal(t, "v", v)
}
