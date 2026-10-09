package anonymous

import (
	"github.com/pigeonbox/core/pkg/memkv/redisshim"
)

// memoryKV 单机内存模式下 redisKV 的等价兜底（redis.host 为空时 NewService 用，
// 取件码映射/展示信息存进程内 TTL KV：不跨进程共享、重启即失，DB 仍是唯一真相源）。
// 实现已收口 pkg/memkv/redisshim——此前本域与 presign 各持一份几乎相同的拷贝
// （SetArgs 还只在 anonymous 侧），2026-10-10 合并去重，命令面取两侧超集；
// 语义细节见 redisshim 包注释。别名保持域内既有用法（含测试的类型断言）不变。
type memoryKV = redisshim.KV

func newMemoryKV() memoryKV { return redisshim.New() }
