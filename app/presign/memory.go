package presign

import (
	"context"
	"fmt"
	"time"

	"github.com/filescodebox/core/pkg/memkv"
	"github.com/redis/go-redis/v9"
)

// memoryKV 单机内存模式下 redisKV 的等价实现（redis.host 为空时 NewService 兜底）。
// 直传会话存进程内 TTL KV：不跨进程共享、重启即失（未完成直传作废，客户端
// 重新 Init 即可）；容量/清扫参数用 memkv 包默认值，对齐 lockout 内存兜底先例。
// 命令结果用 go-redis 的 New*Result 构造器包装，redis.Nil 表不存在——
// 服务层既有的 redis.Nil 判断两种模式通用，无分叉。
type memoryKV struct {
	s *memkv.Store
}

func newMemoryKV() memoryKV {
	return memoryKV{s: memkv.New()}
}

// toStr 归一化写入值：消费面传 []byte（meta JSON）与 string，其余兜底格式化。
func toStr(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []byte:
		return string(t)
	default:
		return fmt.Sprint(v)
	}
}

func (m memoryKV) Get(_ context.Context, key string) *redis.StringCmd {
	if v, ok := m.s.Get(key); ok {
		return redis.NewStringResult(v, nil)
	}
	return redis.NewStringResult("", redis.Nil)
}

func (m memoryKV) Set(_ context.Context, key string, value any, expiration time.Duration) *redis.StatusCmd {
	m.s.Set(key, toStr(value), expiration)
	return redis.NewStatusResult("OK", nil)
}

func (m memoryKV) Del(_ context.Context, keys ...string) *redis.IntCmd {
	return redis.NewIntResult(int64(m.s.Del(keys...)), nil)
}
