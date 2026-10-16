package agent

import (
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
	"unicode"
)

// ErrLocalAddrWhitespace 本地地址含空白/控制字符（复制粘贴尾部空格是常见来源）。
var ErrLocalAddrWhitespace = errors.New("本地地址里不能有空格/换行，请检查是不是从别处复制时带了尾部空白")

// NormalizeLocalAddrInput 入口归一化：经最终 URL 解析校验后返回干净地址（保留显式 scheme）。
//
// ★ 不带 scheme 的 host:port 原样返回（后续补 http://）。
// ★ 一律按 url.Parse 的 authority 判定；拒绝 userinfo / path / query / fragment。
func NormalizeLocalAddrInput(raw string) (string, error) {
	hadScheme := strings.Contains(raw, "://")
	u, err := ParseLocalBaseURL(raw)
	if err != nil {
		return "", err
	}
	hostPort := u.Host
	if hadScheme {
		return u.Scheme + "://" + hostPort, nil
	}
	// 无 scheme 输入：若 Parse 补了默认端口展示，仍只回 host:port（与用户输入契约一致）
	return hostPort, nil
}

// ParseLocalBaseURL 把 -local 解析成带 scheme 的基址。
//
// ★ 无 scheme → http；显式 https/http 保留；绝不返回 (nil, nil)。
// ★ 目标主机以 u.Hostname() 为准（防 127.0.0.1:3000@8.8.8.8 绕过）；
//
//	显式拒绝 userinfo；按 host:port 契约拒绝 path/query/fragment。
func ParseLocalBaseURL(localAddr string) (*url.URL, error) {
	addr := strings.TrimSpace(localAddr)
	if addr == "" {
		return nil, errors.New("本地地址为空")
	}
	if containsWhitespaceOrControl(addr) {
		return nil, ErrLocalAddrWhitespace
	}

	raw := addr
	if !strings.Contains(addr, "://") {
		raw = "http://" + addr
	} else {
		scheme, _, err := splitLocalScheme(addr)
		if err != nil {
			return nil, err
		}
		_ = scheme
	}

	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if err := validateParsedLocalURL(u); err != nil {
		return nil, err
	}
	return u, nil
}

// validateParsedLocalURL 对最终解析结果做统一契约校验（CheckLocalAddr / Parse 共用）。
func validateParsedLocalURL(u *url.URL) error {
	if u == nil {
		return errors.New("本地地址解析结果为空")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("本地地址只支持 http/https，收到 %q", u.Scheme)
	}
	// ★ userinfo 会让 Hostname() 与 SplitHostPort 看到不同主机 —— 必须显式拒绝
	if u.User != nil {
		return errors.New("本地地址不能含 userinfo（如 user@host 或 127.0.0.1:3000@8.8.8.8）")
	}
	if u.Path != "" && u.Path != "/" {
		return fmt.Errorf("本地地址不能含 path（收到 %q）；请只写 host:port 或 https://host:port", u.Path)
	}
	if u.RawQuery != "" {
		return errors.New("本地地址不能含 query")
	}
	if u.Fragment != "" {
		return errors.New("本地地址不能含 fragment")
	}
	if u.Hostname() == "" {
		return errors.New("本地地址缺少主机名")
	}
	// 端口若出现必须是纯数字；缺省端口允许（历史：纯 host）
	if p := u.Port(); p != "" {
		n, convErr := strconv.Atoi(p)
		if convErr != nil || n < 1 || n > 65535 {
			return fmt.Errorf("端口号必须是 1~65535 的数字，当前是 %q", p)
		}
	}
	return nil
}

// splitLocalScheme 剥可选 http/https；其它 scheme 明确报错。
func splitLocalScheme(addr string) (scheme, hostPort string, err error) {
	idx := strings.Index(addr, "://")
	if idx < 0 {
		return "", addr, nil
	}
	scheme = strings.ToLower(addr[:idx])
	hostPort = addr[idx+3:]
	switch scheme {
	case "http", "https":
		if hostPort == "" {
			return "", "", errors.New("本地地址缺少主机名")
		}
		return scheme, hostPort, nil
	default:
		return "", "", fmt.Errorf("本地地址只支持 http:// 或 https://，收到 scheme %q", scheme)
	}
}

func containsWhitespaceOrControl(s string) bool {
	for _, r := range s {
		// ★ unicode.IsSpace 覆盖 NBSP / 全角空格等；IsControl 覆盖 \t\n\r
		if unicode.IsSpace(r) || unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// hostPortForDial 给 net.Dial 用：去掉 http(s)://，只留 host:port。
func hostPortForDial(localAddr string) string {
	u, err := ParseLocalBaseURL(localAddr)
	if err != nil {
		_, hostPort, splitErr := splitLocalScheme(localAddr)
		if splitErr != nil || hostPort == "" {
			return localAddr
		}
		return hostPort
	}
	return dialHostPort(u)
}

// dialHostPort 从已解析 URL 得到 dial 用的 host:port（补默认 80/443）。
func dialHostPort(u *url.URL) string {
	host := u.Host
	if u.Port() == "" {
		port := "80"
		if u.Scheme == "https" {
			port = "443"
		}
		host = net.JoinHostPort(u.Hostname(), port)
	}
	return host
}

// checkHostnameAllowed 用最终 Hostname 判回环 / 私网 / 公网。
func checkHostnameAllowed(hostname string) error {
	host := strings.Trim(hostname, "[]")
	if host == "" {
		return errors.New("缺少主机名（例：127.0.0.1:3000）")
	}
	ip := net.ParseIP(host)
	if ip == nil {
		if host != "localhost" {
			return fmt.Errorf("不支持主机名 %q：请用 IP 或 localhost（用域名会把 DNS 解析也变成一条不确定的链路）", host)
		}
		return warnPrivate("localhost（回环）")
	}
	if ip.IsLoopback() {
		return nil
	}
	if ip.IsPrivate() || ip.IsLinkLocalUnicast() {
		return warnPrivate(host)
	}
	return fmt.Errorf("拒绝连接公网地址 %s：-local 只允许本机或内网地址", host)
}

// isLoopbackHost 判断 host（可带端口）是否回环。
func isLoopbackHost(hostPort string) bool {
	host := hostPort
	if h, _, err := net.SplitHostPort(hostPort); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
