package middleware

import (
	"net"
	"strings"
	"sync"

	"github.com/cloudwego/hertz/pkg/app"
)

// 可信代理配置（bootstrap 从 security.trusted_proxies 注入）。
//
// 安全语义：只有当"直连对端"（RemoteAddr）落在可信代理网段内时，才采信
// X-Forwarded-For / X-Real-IP；否则一律以直连地址为准。
// 这样反向代理部署（配置了 CIDR）下能取到真实客户端 IP，
// 而直连部署下伪造 XFF 无法绕过 IP 维度限流/锁定。
var (
	trustedMu      sync.RWMutex
	trustedCIDRs   []*net.IPNet
	trustedEnabled bool
)

// SetTrustedProxies 配置可信代理网段（CIDR 或单 IP）。
// 空列表 = 不信任任何代理头（直连部署的安全默认）。
func SetTrustedProxies(cidrs []string) error {
	trustedMu.Lock()
	defer trustedMu.Unlock()
	parsed := make([]*net.IPNet, 0, len(cidrs))
	for _, c := range cidrs {
		c = strings.TrimSpace(c)
		if c == "" {
			continue
		}
		if !strings.Contains(c, "/") {
			c += "/32"
		}
		_, ipnet, err := net.ParseCIDR(c)
		if err != nil {
			return err
		}
		parsed = append(parsed, ipnet)
	}
	trustedCIDRs = parsed
	trustedEnabled = len(parsed) > 0
	return nil
}

// isTrustedProxy 判断 addr（host:port 或裸 IP）是否在可信代理网段内
func isTrustedProxy(addr string) bool {
	trustedMu.RLock()
	defer trustedMu.RUnlock()
	if !trustedEnabled {
		return false
	}
	host := addr
	if h, _, err := net.SplitHostPort(addr); err == nil {
		host = h
	}
	ip := net.ParseIP(strings.TrimSpace(host))
	if ip == nil {
		return false
	}
	for _, cidr := range trustedCIDRs {
		if cidr.Contains(ip) {
			return true
		}
	}
	return false
}

// ClientIP 解析客户端真实 IP（可信代理解析，XFF 从右向左）。
//
// 规则：
//  1. 直连地址不在可信代理网段 → 返回直连地址（忽略一切代理头）
//  2. 直连地址可信 → 从 X-Forwarded-For 最右端向左扫描，
//     跳过可信代理地址，返回第一个不可信地址（即真实客户端）
//  3. 无 XFF 时回退 X-Real-IP（仅当直连可信），再回退直连地址
func ClientIP(c *app.RequestContext) string {
	// RemoteAddr 返回的 net.Addr 接口值恒非 nil，直接取字符串
	return ResolveClientIP(remoteAddrString(c.RemoteAddr()),
		string(c.GetHeader("X-Forwarded-For")),
		string(c.GetHeader("X-Real-IP")))
}

// ResolveClientIP 可信代理解析核心（纯函数，便于单测覆盖伪造场景）。
// remote 为直连对端地址（host:port 或裸 IP）；xff/xri 为原始代理头。
//
// 规则：
//  1. 直连地址不在可信代理网段 → 采信直连，忽略一切代理头
//     （直连部署下伪造 XFF 无法绕过 IP 维度限流/锁定）
//  2. 直连可信 → XFF 从右向左扫描，跳过可信代理，取第一个不可信地址（真实客户端）
//  3. 无 XFF 回退 X-Real-IP（仅当直连可信）
func ResolveClientIP(remote, xff, xri string) string {
	if remote == "" {
		return "unknown"
	}
	if !isTrustedProxy(remote) {
		return stripPort(remote)
	}

	if xff != "" {
		parts := strings.Split(xff, ",")
		for i := len(parts) - 1; i >= 0; i-- {
			cand := strings.TrimSpace(parts[i])
			if cand == "" {
				continue
			}
			if isTrustedProxy(cand) {
				continue
			}
			return stripPort(cand)
		}
		// 链上全部可信（代理嵌套过深），取最左端
		if cand := strings.TrimSpace(parts[0]); cand != "" {
			return stripPort(cand)
		}
	}

	if xri = strings.TrimSpace(xri); xri != "" {
		// 2026-10-05 审计 P2：X-Real-IP 原样返回会让可信代理透传的任意字符串
		// 进入限流/锁定键与审计日志（伪造 IP 永不命中计数 = 绕过；还可注入
		// 任意内容污染日志）。非合法 IP 一律回退直连地址。
		if net.ParseIP(stripPort(xri)) != nil {
			return xri
		}
	}
	return stripPort(remote)
}

// remoteAddrString net.Addr 安全取字符串（nil 防御）
func remoteAddrString(addr interface{ String() string }) string {
	if addr == nil {
		return ""
	}
	return addr.String()
}

// stripPort 去掉 IP:port 的端口部分（IPv6 [::1]:8080 同样处理）
func stripPort(addr string) string {
	if h, _, err := net.SplitHostPort(addr); err == nil {
		return h
	}
	return addr
}
