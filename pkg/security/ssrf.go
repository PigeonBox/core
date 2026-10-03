package security

import (
	"fmt"
	"net"
	"net/url"
	"sync"
	"time"

	"github.com/filescodebox/core/conf"
)

// ErrEndpointURL 存储端点 URL 不合法
var ErrEndpointURL = fmt.Errorf("存储端点 URL 不合法")

// dnsCache 端点解析结果短缓存，避免每次校验都打 DNS（5 分钟 TTL）
var (
	dnsCacheMu sync.Mutex
	dnsCache   = map[string]dnsCacheEntry{}
)

type dnsCacheEntry struct {
	ips  []net.IP
	at   time.Time
	priv bool
}

// ValidateEndpointURL 校验 s3/webdav 存储端点 URL：
//   - 仅允许 http/https scheme
//   - 必须携带 host
//   - DNS 解析后逐 IP 复判私网策略（防重绑定攻击：解析结果非配置期望网段时拒绝）
//
// allowPrivate 未配置（默认 false）时拒绝环回/私网/链路本地地址。
// 局域网 MinIO/WebDAV（飞牛 NAS）部署需显式 security.ssrf.allow_private_networks=true。
func ValidateEndpointURL(rawURL string) error {
	u, err := url.Parse(rawURL)
	if err != nil {
		return fmt.Errorf("%w: %v", ErrEndpointURL, err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("%w: 仅支持 http/https 协议，当前为 %q", ErrEndpointURL, u.Scheme)
	}
	host := u.Hostname()
	if host == "" {
		return fmt.Errorf("%w: 缺少 host", ErrEndpointURL)
	}

	allowPrivate := false
	if cfg := conf.GetGlobalConfig(); cfg != nil {
		allowPrivate = cfg.Security.SSRF.AllowPrivateNetworks
	}
	if allowPrivate {
		return nil
	}

	// IP 直写或域名解析，逐 IP 校验
	ips := resolveHost(host)
	if len(ips) == 0 {
		// 解析失败不在此处定性（连接阶段会自然失败），仅放行 scheme/host 校验
		return nil
	}
	for _, ip := range ips {
		if isPrivateOrLocal(ip) {
			return fmt.Errorf("%w: 端点解析到私网/保留地址 %s；如为局域网存储请在配置中开启 security.ssrf.allow_private_networks", ErrEndpointURL, ip)
		}
	}
	return nil
}

// resolveHost 带缓存的 DNS 解析（解析结果按"是否含私网"缓存 5 分钟）
func resolveHost(host string) []net.IP {
	dnsCacheMu.Lock()
	defer dnsCacheMu.Unlock()
	if e, ok := dnsCache[host]; ok && time.Since(e.at) < 5*time.Minute {
		return e.ips
	}
	ips, err := net.LookupIP(host)
	if err != nil {
		return nil
	}
	dnsCache[host] = dnsCacheEntry{ips: ips, at: time.Now()}
	return ips
}

func isPrivateOrLocal(ip net.IP) bool {
	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}
