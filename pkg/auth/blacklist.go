package auth

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"sync"
	"time"

	"github.com/redis/go-redis/v9"
)

// tokenBlacklist JWT 注销黑名单（logout 端点写入，认证中间件查询）。
// 键 = token 的 SHA-256（避免明文 token 进入存储），TTL = token 剩余有效期。
// Redis 可用时多实例共享；否则进程内存兜底（单实例部署语义不变）。
type tokenBlacklist struct {
	rdb *redis.Client

	memMu sync.Mutex
	mem   map[string]time.Time
}

// 供 auth 包内部单例使用
var globalBlacklist *tokenBlacklist

// SetBlacklistRedis 注入 Redis（bootstrap 调用；nil = 纯内存模式）
func SetBlacklistRedis(rdb *redis.Client) {
	globalBlacklist = &tokenBlacklist{rdb: rdb, mem: map[string]time.Time{}}
}

func getBlacklist() *tokenBlacklist {
	if globalBlacklist == nil {
		globalBlacklist = &tokenBlacklist{mem: map[string]time.Time{}}
	}
	return globalBlacklist
}

func tokenKey(token string) string {
	sum := sha256.Sum256([]byte(token))
	return "fcb:jwt:bl:" + hex.EncodeToString(sum[:])
}

// RevokeToken 注销 token（至其自然过期前一直拦截）
func RevokeToken(ctx context.Context, token string, remaining time.Duration) {
	if token == "" || remaining <= 0 {
		return
	}
	bl := getBlacklist()
	if bl.rdb != nil {
		bl.rdb.Set(ctx, tokenKey(token), "1", remaining)
		return
	}
	bl.memMu.Lock()
	defer bl.memMu.Unlock()
	bl.mem[tokenKey(token)] = time.Now().Add(remaining)
}

// IsTokenRevoked 查询 token 是否已注销
func IsTokenRevoked(ctx context.Context, token string) bool {
	if token == "" {
		return false
	}
	bl := getBlacklist()
	if bl.rdb != nil {
		n, err := bl.rdb.Exists(ctx, tokenKey(token)).Result()
		return err == nil && n > 0
	}
	bl.memMu.Lock()
	defer bl.memMu.Unlock()
	until, ok := bl.mem[tokenKey(token)]
	if !ok {
		return false
	}
	if time.Now().After(until) {
		delete(bl.mem, tokenKey(token))
		return false
	}
	return true
}
