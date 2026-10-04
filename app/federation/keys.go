package federation

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// loadOrCreateKey 加载或生成节点身份密钥（Ed25519）。
// 文件格式：32 字节 seed 的 hex（0600）。node_id 即公钥 hex；
// 密钥丢失=联邦身份更换，registry 侧旧条目随租约 TTL 自然消亡。
func loadOrCreateKey(path string) (ed25519.PrivateKey, error) {
	if b, err := os.ReadFile(path); err == nil {
		seed, err := hex.DecodeString(strings.TrimSpace(string(b)))
		if err == nil && len(seed) == ed25519.SeedSize {
			return ed25519.NewKeyFromSeed(seed), nil
		}
		// 损坏文件按不存在处理：重新生成（旧身份自然淘汰）
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("读取密钥: %w", err)
	}

	_, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("生成密钥: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("创建密钥目录: %w", err)
	}
	if err := os.WriteFile(path, []byte(hex.EncodeToString(priv.Seed())), 0o600); err != nil {
		return nil, fmt.Errorf("持久化密钥: %w", err)
	}
	return priv, nil
}
