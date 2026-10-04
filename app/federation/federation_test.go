package federation

import (
	"crypto/ed25519"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/filescodebox/core/conf"
)

// ---- 契约锁定 ----

// TestPayloadContract 锁定与 p2p 仓的签名负载契约（逐字节）。
// p2p 侧实现见其 internal/registry/sign.go；任何一侧变动必须双侧同步。
func TestPayloadContract(t *testing.T) {
	got := string(buildPayload("id", "https://a", "n", "1", "download", "3600", "nc", "1700000000"))
	want := "id\nhttps://a\nn\n1\ndownload\n3600\nnc\n1700000000"
	if got != want {
		t.Fatalf("注册负载契约漂移:\n got=%q\nwant=%q", got, want)
	}
	got = string(buildPayload("id", "abc", "1700000000", "0", "1700000001"))
	want = "id\nabc\n1700000000\n0\n1700000001"
	if got != want {
		t.Fatalf("公告负载契约漂移:\n got=%q\nwant=%q", got, want)
	}
	got = string(buildPayload("id", "abc", "1700000001"))
	want = "id\nabc\n1700000001"
	if got != want {
		t.Fatalf("撤销负载契约漂移:\n got=%q\nwant=%q", got, want)
	}
	// SHA-256("") 标准期望值,锁定 codeHash 与 p2p 侧一致
	if codeHash("") != "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855" {
		t.Fatalf("codeHash 契约漂移: %s", codeHash(""))
	}
}

// ---- 测试用假 registry（按 p2p 服务端语义验签） ----

type fakeRegistry struct {
	mu        sync.Mutex
	nodes     map[string]registerReq
	announces map[string]announceReq
	revoked   []string
}

func newFakeRegistry(t *testing.T) (*fakeRegistry, *httptest.Server) {
	f := &fakeRegistry{nodes: map[string]registerReq{}, announces: map[string]announceReq{}}
	verify := func(nodeID, sigB64 string, parts ...string) {
		t.Helper()
		pubRaw, err := hex.DecodeString(nodeID)
		if err != nil || len(pubRaw) != ed25519.PublicKeySize {
			t.Fatalf("node_id 非法: %s", nodeID)
		}
		sig, err := base64.StdEncoding.DecodeString(sigB64)
		if err != nil {
			t.Fatalf("sig 非法: %v", err)
		}
		payload := buildPayload(parts...)
		if !ed25519.Verify(ed25519.PublicKey(pubRaw), payload, sig) {
			t.Fatalf("签名校验失败(契约不一致?): payload=%q", string(payload))
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/nodes/register", func(w http.ResponseWriter, r *http.Request) {
		var req registerReq
		_ = json.NewDecoder(r.Body).Decode(&req)
		verify(req.NodeID, req.Sig, req.NodeID, req.URL, req.Name, req.Version,
			strings.Join(req.Caps, ","), fmt.Sprint(req.TTLSeconds), req.Nonce, fmt.Sprint(req.TS))
		f.mu.Lock()
		f.nodes[req.NodeID] = req
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"node_id": req.NodeID})
	})
	mux.HandleFunc("POST /v1/announces", func(w http.ResponseWriter, r *http.Request) {
		var req announceReq
		_ = json.NewDecoder(r.Body).Decode(&req)
		verify(req.NodeID, req.Sig, req.NodeID, req.CodeHash, fmt.Sprint(req.ExpiresAt), fmt.Sprint(req.SizeHint), fmt.Sprint(req.TS))
		f.mu.Lock()
		f.announces[req.CodeHash] = req
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"code_hash": req.CodeHash})
	})
	mux.HandleFunc("DELETE /v1/announces/", func(w http.ResponseWriter, r *http.Request) {
		hash := strings.TrimPrefix(r.URL.Path, "/v1/announces/")
		var req struct {
			NodeID string `json:"node_id"`
			TS     int64  `json:"ts"`
			Sig    string `json:"sig"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		verify(req.NodeID, req.Sig, req.NodeID, hash, fmt.Sprint(req.TS))
		f.mu.Lock()
		delete(f.announces, hash)
		f.revoked = append(f.revoked, hash)
		f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})
	mux.HandleFunc("GET /v1/resolve/", func(w http.ResponseWriter, r *http.Request) {
		hash := strings.TrimPrefix(r.URL.Path, "/v1/resolve/")
		f.mu.Lock()
		a, ok := f.announces[hash]
		node, nodeOK := f.nodes[a.NodeID]
		f.mu.Unlock()
		if !ok || !nodeOK {
			w.WriteHeader(http.StatusNotFound)
			_ = json.NewEncoder(w).Encode(map[string]string{"error": "not found"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{
			"node_id": node.NodeID, "url": node.URL, "name": node.Name,
			"expires_at": a.ExpiresAt, "size_hint": a.SizeHint,
		})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return f, srv
}

// ---- Service 测试 ----

func newTestService(t *testing.T, regURL string) *Service {
	t.Helper()
	cfg := conf.FederationConfig{
		Enabled:                true,
		RegistryURL:            regURL,
		PublicURL:              "https://node-a.example.com",
		NodeKeyPath:            filepath.Join(t.TempDir(), "federation.key"),
		AnnounceMinEntropyBits: 40,
	}
	s, err := NewService(cfg, "测试站")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	return s
}

func TestNewServiceValidation(t *testing.T) {
	if _, err := NewService(conf.FederationConfig{Enabled: false}, ""); err == nil {
		t.Fatal("disabled 应拒绝构造")
	}
	if _, err := NewService(conf.FederationConfig{Enabled: true}, ""); err == nil {
		t.Fatal("缺 registry_url 应报错")
	}
	if _, err := NewService(conf.FederationConfig{Enabled: true, RegistryURL: "http://r"}, ""); err == nil {
		t.Fatal("缺 public_url 应报错")
	}
	if _, err := NewService(conf.FederationConfig{Enabled: true, RegistryURL: "ftp://r", PublicURL: "https://a"}, ""); err == nil {
		t.Fatal("非法 scheme 应报错")
	}
}

func TestKeyPersistence(t *testing.T) {
	path := filepath.Join(t.TempDir(), "federation.key")
	k1, err := loadOrCreateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	k2, err := loadOrCreateKey(path)
	if err != nil {
		t.Fatal(err)
	}
	if !equalKeys(k1, k2) {
		t.Fatal("重载密钥应一致")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("密钥文件权限应为 0600,得到 %v", info.Mode().Perm())
	}
}

func TestEntropyGate(t *testing.T) {
	if !announceAllowed("Ab3!xY9qZw2#", 40) {
		t.Fatal("12 位混合字符应过 40bit 门槛")
	}
	if announceAllowed("123456", 40) {
		t.Fatal("6 位数字码不应出站")
	}
	if announceAllowed("abcdef", 40) {
		t.Fatal("6 位小写不应出站")
	}
	if !announceAllowed("correct-horse-battery", 40) {
		t.Fatal("长短语应过门槛")
	}
	if estimateEntropyBits("") != 0 {
		t.Fatal("空串熵为 0")
	}
}

func TestShareCreatedAnnouncesAndResolves(t *testing.T) {
	fake, srv := newFakeRegistry(t)
	s := newTestService(t, srv.URL)

	code := "federated-code-1234567890"
	s.ShareCreated(code, nil)
	s.tick() // 注册节点(resolve 依赖节点在册)并确认公告

	// 异步公告,轮询等待
	waitFor(t, 2*time.Second, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.announces) == 1
	})

	info, err := s.Resolve(code)
	if err != nil || info == nil {
		t.Fatalf("resolve: info=%+v err=%v", info, err)
	}
	if info.URL != "https://node-a.example.com" || info.NodeID != s.NodeID() {
		t.Fatalf("resolve 结果不符: %+v", info)
	}
	if info, err := s.Resolve("不存在的口令-xyz-9999"); err != nil || info != nil {
		t.Fatalf("未接入应 (nil,nil),得到 info=%+v err=%v", info, err)
	}
}

func TestShareCreatedEntropyGateSkips(t *testing.T) {
	fake, srv := newFakeRegistry(t)
	s := newTestService(t, srv.URL)
	s.ShareCreated("123456", nil) // 低熵:不出站
	time.Sleep(200 * time.Millisecond)
	fake.mu.Lock()
	n := len(fake.announces)
	fake.mu.Unlock()
	if n != 0 {
		t.Fatalf("低熵码不应公告,实际 %d 条", n)
	}
}

func TestShareDeletedRevokes(t *testing.T) {
	fake, srv := newFakeRegistry(t)
	s := newTestService(t, srv.URL)
	code := "revoke-target-code-123"
	s.ShareCreated(code, nil)
	waitFor(t, 2*time.Second, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.announces) == 1
	})

	s.ShareDeleted(code)
	waitFor(t, 2*time.Second, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.revoked) == 1
	})
	if info, err := s.Resolve(code); err != nil || info != nil {
		t.Fatalf("撤销后应 (nil,nil),得到 info=%+v err=%v", info, err)
	}
}

func TestTickRegistersAndReannounces(t *testing.T) {
	fake, srv := newFakeRegistry(t)
	s := newTestService(t, srv.URL)
	code := "selfheal-code-123456789"
	s.ShareCreated(code, nil)
	s.tick() // 手动触发:注册 + 全量补公告

	fake.mu.Lock()
	registered, announced := len(fake.nodes), len(fake.announces)
	fake.mu.Unlock()
	if registered != 1 {
		t.Fatalf("tick 应注册节点,实际 %d", registered)
	}
	if announced != 1 {
		t.Fatalf("tick 应补公告,实际 %d", announced)
	}
}

func TestRegistryDownDoesNotBlock(t *testing.T) {
	// 指向必然失败的地址:构造/钩子/Stop 均不得 panic 或长时间阻塞
	s, err := NewService(conf.FederationConfig{
		Enabled: true, RegistryURL: "http://127.0.0.1:1", PublicURL: "https://x",
		NodeKeyPath: filepath.Join(t.TempDir(), "k"), AnnounceMinEntropyBits: 40,
	}, "x")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Stop)
	done := make(chan struct{})
	go func() {
		s.ShareCreated("offline-code-12345678", nil)
		s.tick()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("registry 不可达时钩子阻塞超过 20s")
	}
}

// ---- 小件 ----

func waitFor(t *testing.T, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("等待条件超时")
}

func equalKeys(a, b ed25519.PrivateKey) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
