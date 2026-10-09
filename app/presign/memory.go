package presign

import (
	"github.com/pigeonbox/core/pkg/memkv/redisshim"
)

// memoryKV 单机内存模式下 redisKV 的等价兜底（redis.host 为空时 NewService 用）。
// 直传会话存进程内 TTL KV：不跨进程共享、重启即失（未完成直传作废，客户端
// 重新 Init 即可）。实现已收口 pkg/memkv/redisshim——此前本域与 anonymous
// 各持一份几乎相同的拷贝，2026-10-10 合并去重；语义细节见 redisshim 包注释。
type memoryKV = redisshim.KV

func newMemoryKV() memoryKV { return redisshim.New() }
