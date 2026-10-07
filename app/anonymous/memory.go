package anonymous

import (
	"context"
	"fmt"
	"time"

	"github.com/pigeonbox/core/pkg/memkv"
	"github.com/redis/go-redis/v9"
)

// memoryKV 单机内存模式下 redisKV 的等价实现（redis.host 为空时 NewService 兜底，
// 取件码映射/展示信息存进程内 TTL KV：不跨进程共享、重启即失，DB 仍是唯一真相源）。
// 容量/清扫参数用 memkv 包默认值（上限+周期清扫，对齐 lockout 内存兜底先例）。
// 命令结果用 go-redis 的 New*Result 构造器包装，redis.Nil 表不存在——
// 服务层既有的 redis.Nil 判断两种模式通用，无分叉。
type memoryKV struct {
	s *memkv.Store
}

func newMemoryKV() memoryKV {
	return memoryKV{s: memkv.New()}
}

// toStr 归一化写入值：消费面传 string（映射/展示信息）与 []byte（预留），其余兜底格式化。
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

// SetArgs 消费面仅用 Mode:"NX"+TTL（GenerateCode 唯一性），其余选项不支持（fail-loud）。
func (m memoryKV) SetArgs(_ context.Context, key string, value any, a redis.SetArgs) *redis.StatusCmd {
	switch a.Mode {
	case "NX":
		if !m.s.SetNX(key, toStr(value), a.TTL) {
			// 已存在：与 Redis NX 未命中语义一致（服务层按 redis.Nil 重试换码）
			return redis.NewStatusResult("", redis.Nil)
		}
		return redis.NewStatusResult("OK", nil)
	case "":
		m.s.Set(key, toStr(value), a.TTL)
		return redis.NewStatusResult("OK", nil)
	default:
		return redis.NewStatusResult("", fmt.Errorf("memoryKV: unsupported SetArgs mode %q", a.Mode))
	}
}

func (m memoryKV) Del(_ context.Context, keys ...string) *redis.IntCmd {
	return redis.NewIntResult(int64(m.s.Del(keys...)), nil)
}
