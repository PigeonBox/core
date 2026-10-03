package middleware

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
	"github.com/filescodebox/contracts/errcode"
	"github.com/filescodebox/core/conf"
)

// Lockout 登录/取件失败计数锁定（防爆破，非 QPS 语义）。
//
// 维度由调用方决定（建议 "ip"、"user:xxx"、"code:xxx" 组合键）：
//   - 窗口内失败次数达到 MaxAttempts → 锁定 LockSeconds
//   - Redis 可用时计数/锁定共享（多实例生效）；否则进程内存兜底
type Lockout struct {
	rdb *redis.Client
	cfg conf.LockoutConfig

	mu      sync.Mutex
	memHit  map[string]time.Time // key → 锁定截止时间
	memFail map[string]*failWindow
}

type failWindow struct {
	count     int
	windowEnd time.Time
}

const (
	lockKeyPrefix = "fcb:lock:hit:"   // 锁定标记
	failKeyPrefix = "fcb:lock:count:" // 失败计数
)

// NewLockout 创建失败锁定器。rdb 为 nil 或配置未启用时仍返回可用实例（内存模式）。
func NewLockout(rdb *redis.Client) *Lockout {
	cfg := conf.LockoutConfig{
		Enabled:       true,
		MaxAttempts:   10,
		WindowSeconds: 300,
		LockSeconds:   600,
	}
	if c := conf.GetGlobalConfig(); c != nil {
		if c.Security.Lockout.MaxAttempts > 0 {
			cfg = c.Security.Lockout
		}
	}
	return &Lockout{
		rdb:     rdb,
		cfg:     cfg,
		memHit:  map[string]time.Time{},
		memFail: map[string]*failWindow{},
	}
}

// Enabled 是否启用
func (l *Lockout) Enabled() bool { return l.cfg.Enabled }

// CheckLocked 返回 (剩余锁定秒数, 是否被锁)。未启用恒返回 (0,false)。
func (l *Lockout) CheckLocked(ctx context.Context, key string) (int, bool) {
	if !l.cfg.Enabled {
		return 0, false
	}
	if l.rdb != nil {
		ttl, err := l.rdb.TTL(ctx, lockKeyPrefix+key).Result()
		if err == nil && ttl > 0 {
			return int(ttl.Seconds()) + 1, true
		}
		return 0, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if until, ok := l.memHit[key]; ok {
		if remain := time.Until(until); remain > 0 {
			return int(remain.Seconds()) + 1, true
		}
		delete(l.memHit, key)
	}
	return 0, false
}

// RecordFailure 记录一次失败。返回 (是否触发锁定, 锁定秒数)。
func (l *Lockout) RecordFailure(ctx context.Context, key string) (bool, int) {
	if !l.cfg.Enabled {
		return false, 0
	}
	window := time.Duration(l.cfg.WindowSeconds) * time.Second
	lock := time.Duration(l.cfg.LockSeconds) * time.Second

	if l.rdb != nil {
		count, err := l.rdb.Incr(ctx, failKeyPrefix+key).Result()
		if err == nil {
			if count == 1 {
				l.rdb.Expire(ctx, failKeyPrefix+key, window)
			}
			if count >= int64(l.cfg.MaxAttempts) {
				l.rdb.Set(ctx, lockKeyPrefix+key, "1", lock)
				l.rdb.Del(ctx, failKeyPrefix+key)
				return true, l.cfg.LockSeconds
			}
			return false, 0
		}
		// Redis 故障 → 内存兜底
	}

	l.mu.Lock()
	defer l.mu.Unlock()
	now := time.Now()
	w, ok := l.memFail[key]
	if !ok || now.After(w.windowEnd) {
		w = &failWindow{count: 0, windowEnd: now.Add(window)}
		l.memFail[key] = w
	}
	w.count++
	if w.count >= l.cfg.MaxAttempts {
		l.memHit[key] = now.Add(lock)
		delete(l.memFail, key)
		return true, l.cfg.LockSeconds
	}
	return false, 0
}

// Reset 成功后清空失败计数。
func (l *Lockout) Reset(ctx context.Context, key string) {
	if l.rdb != nil {
		l.rdb.Del(ctx, failKeyPrefix+key)
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	delete(l.memFail, key)
}

// defaultLockout 全局单例（bootstrap 注入 Redis 后创建）
var defaultLockout *Lockout

// InitDefaultLockout 初始化全局锁定器（bootstrap 调用一次）
func InitDefaultLockout(rdb *redis.Client) *Lockout {
	defaultLockout = NewLockout(rdb)
	return defaultLockout
}

// GetDefaultLockout 获取全局锁定器（未初始化时用纯内存模式兜底，保证 handler 可用）
func GetDefaultLockout() *Lockout {
	if defaultLockout == nil {
		defaultLockout = NewLockout(nil)
	}
	return defaultLockout
}

// LockedError 被锁定错误（携带剩余秒数）
type LockedError struct {
	RemainingSeconds int
}

func (e *LockedError) Error() string {
	return fmt.Sprintf("尝试过于频繁，已临时锁定，请 %d 秒后重试", e.RemainingSeconds)
}

// ErrCode 转换为统一错误码
func (e *LockedError) ErrCode() int { return errcode.CodeTooManyAttempts }

// FormatLockKey 组合锁定维度键（值做转义避免键冲突）
func FormatLockKey(parts ...string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += "|"
		}
		out += strconv.Quote(p)
	}
	return out
}
