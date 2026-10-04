package federation

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// 与 p2p 仓（github.com/filescodebox/p2p）v1 API 的通讯客户端。
//
// 签名负载契约（逐字节，Ed25519 + base64 std；字段以 "\n" 连接，缺省字段
// 空串占位——由 TestPayloadContract 锁定，p2p 侧变动必须双侧同步）：
//
//	注册/心跳:  node_id | url | name | version | caps | ttl_seconds | nonce | ts
//	公告:       node_id | code_hash | expires_at | size_hint | ts
//	撤销公告:   node_id | code_hash | ts
//	注销节点:   node_id | ts
const (
	nodeVersion  = "1"        // 节点协议能力版本
	capsDownload = "download" // M2 联邦取件 = HTTP 直连源节点下载
)

type registryClient struct {
	base  string
	http  *http.Client
	nonce func() string
}

func newRegistryClient(base string) *registryClient {
	return &registryClient{
		base:  base,
		http:  &http.Client{Timeout: 5 * time.Second},
		nonce: randomNonce,
	}
}

func randomNonce() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

func buildPayload(parts ...string) []byte {
	return []byte(strings.Join(parts, "\n"))
}

func signPayload(priv ed25519.PrivateKey, payload []byte) string {
	return base64.StdEncoding.EncodeToString(ed25519.Sign(priv, payload))
}

// codeHash 口令 → SHA-256 hex。registry 不接触明文（公开确定性哈希；
// 防枚举靠宣布侧熵门槛 + registry 解析限流，见包注释）。
func codeHash(code string) string {
	sum := sha256.Sum256([]byte(code))
	return hex.EncodeToString(sum[:])
}

func (c *registryClient) do(method, path string, body any, wantStatus int, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = strings.NewReader(string(b))
	}
	req, err := http.NewRequestWithContext(context.Background(), method, c.base+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != wantStatus {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return fmt.Errorf("%s %s: 期望 %d 实际 %d: %s", method, path, wantStatus, resp.StatusCode, b)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

type registerReq struct {
	NodeID     string   `json:"node_id"`
	URL        string   `json:"url"`
	Name       string   `json:"name"`
	Version    string   `json:"version"`
	Caps       []string `json:"caps"`
	TTLSeconds int64    `json:"ttl_seconds"`
	Nonce      string   `json:"nonce"`
	TS         int64    `json:"ts"`
	Sig        string   `json:"sig"`
}

func (c *registryClient) register(_ context.Context, nodeID string, priv ed25519.PrivateKey, publicURL, name string, ttl time.Duration) error {
	ts := time.Now().Unix()
	ttlSec := int64(ttl / time.Second)
	nonce := c.nonce()
	sig := signPayload(priv, buildPayload(nodeID, publicURL, name, nodeVersion, capsDownload,
		strconv.FormatInt(ttlSec, 10), nonce, strconv.FormatInt(ts, 10)))
	return c.do(http.MethodPost, "/v1/nodes/register", registerReq{
		NodeID: nodeID, URL: publicURL, Name: name, Version: nodeVersion,
		Caps: []string{capsDownload}, TTLSeconds: ttlSec, Nonce: nonce, TS: ts, Sig: sig,
	}, http.StatusOK, nil)
}

type announceReq struct {
	NodeID    string `json:"node_id"`
	CodeHash  string `json:"code_hash"`
	ExpiresAt int64  `json:"expires_at"`
	SizeHint  int64  `json:"size_hint"`
	TS        int64  `json:"ts"`
	Sig       string `json:"sig"`
}

func (c *registryClient) announce(_ context.Context, nodeID string, priv ed25519.PrivateKey, hash string, expires time.Time) error {
	ts := time.Now().Unix()
	expiresUnix := expires.Unix()
	sig := signPayload(priv, buildPayload(nodeID, hash, strconv.FormatInt(expiresUnix, 10), "0", strconv.FormatInt(ts, 10)))
	return c.do(http.MethodPost, "/v1/announces", announceReq{
		NodeID: nodeID, CodeHash: hash, ExpiresAt: expiresUnix, SizeHint: 0, TS: ts, Sig: sig,
	}, http.StatusOK, nil)
}

type revokeReq struct {
	NodeID string `json:"node_id"`
	TS     int64  `json:"ts"`
	Sig    string `json:"sig"`
}

func (c *registryClient) revoke(_ context.Context, nodeID string, priv ed25519.PrivateKey, hash string) error {
	ts := time.Now().Unix()
	sig := signPayload(priv, buildPayload(nodeID, hash, strconv.FormatInt(ts, 10)))
	return c.do(http.MethodDelete, "/v1/announces/"+hash, revokeReq{
		NodeID: nodeID, TS: ts, Sig: sig,
	}, http.StatusOK, nil)
}

func (c *registryClient) deregister(_ context.Context, nodeID string, priv ed25519.PrivateKey) error {
	ts := time.Now().Unix()
	sig := signPayload(priv, buildPayload(nodeID, strconv.FormatInt(ts, 10)))
	return c.do(http.MethodDelete, "/v1/nodes/"+nodeID, revokeReq{
		NodeID: nodeID, TS: ts, Sig: sig,
	}, http.StatusOK, nil)
}

// resolve 转查 registry。未接入（404）返回 (nil, nil)；其余非 200 视为错误。
func (c *registryClient) resolve(_ context.Context, code string) (*ResolveInfo, error) {
	req, err := http.NewRequestWithContext(context.Background(), http.MethodGet,
		c.base+"/v1/resolve/"+codeHash(code), nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, nil
	default:
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 256))
		return nil, fmt.Errorf("resolve: 期望 200/404 实际 %d: %s", resp.StatusCode, b)
	}
	var raw struct {
		NodeID    string `json:"node_id"`
		URL       string `json:"url"`
		Name      string `json:"name"`
		ExpiresAt int64  `json:"expires_at"`
		SizeHint  int64  `json:"size_hint"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, fmt.Errorf("resolve 响应解析: %w", err)
	}
	return &ResolveInfo{
		NodeID:    raw.NodeID,
		URL:       raw.URL,
		Name:      raw.Name,
		ExpiresAt: time.Unix(raw.ExpiresAt, 0),
		SizeHint:  raw.SizeHint,
	}, nil
}
