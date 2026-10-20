package netsense

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"syscall"
	"time"
)

// Probe 是一个 Captive Portal 探测点：直连访问 URL，响应正文包含 Expect（或状态码为 204 且 Expect 为空）视为在线。
type Probe struct {
	URL    string
	Expect string
}

// ParseProbe 解析配置中的 "URL|期望正文" 写法；无 | 时表示期望 HTTP 204。
func ParseProbe(s string) Probe {
	u, e, _ := strings.Cut(s, "|")
	return Probe{URL: strings.TrimSpace(u), Expect: strings.TrimSpace(e)}
}

type CaptiveState string

const (
	Online  CaptiveState = "online"
	Captive CaptiveState = "captive" // 被重定向 / 正文被替换：需要网页认证
	Offline CaptiveState = "offline" // 全部探测失败
)

// BoundDialer 返回绑定到指定接口的拨号器（绕过 TUN 等虚拟接口）。ifIndex <= 0 时不绑定。
func BoundDialer(ifIndex int) *net.Dialer {
	d := &net.Dialer{Timeout: 5 * time.Second}
	if ifIndex > 0 {
		d.Control = func(network, address string, c syscall.RawConn) error {
			var serr error
			err := c.Control(func(fd uintptr) { serr = bindToInterface(fd, network, ifIndex) })
			if err != nil {
				return err
			}
			return serr
		}
	}
	return d
}

// PhysicalPath 描述探测走的物理路径：绑定的接口与该接口的 DNS 服务器。
// 绕过 TUN 的同时也必须绕过 fake-ip DNS，否则会拿到只能经 TUN 访问的假地址。
type PhysicalPath struct {
	IfIndex     int
	Nameservers []string
}

func (p PhysicalPath) dialContext() func(ctx context.Context, network, addr string) (net.Conn, error) {
	d := BoundDialer(p.IfIndex)
	if len(p.Nameservers) == 0 {
		return d.DialContext
	}
	r := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
		var last error
		for _, ns := range p.Nameservers {
			c, err := d.DialContext(ctx, network, net.JoinHostPort(ns, "53"))
			if err == nil {
				return c, nil
			}
			last = err
		}
		return nil, last
	}}
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(addr)
		if err != nil {
			return nil, err
		}
		if ip := net.ParseIP(host); ip == nil {
			ips, err := r.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			if len(ips) == 0 {
				return nil, fmt.Errorf("解析 %s 无结果", host)
			}
			host = ips[0].IP.String()
		}
		return d.DialContext(ctx, network, net.JoinHostPort(host, port))
	}
}

// CheckCaptive 依次探测，任一在线即为 Online；有探测被重定向或正文不符即为 Captive；否则 Offline。
// 探测不经过任何代理（包括系统代理与 Velum 自身），并经物理接口及其 DNS 进行。
func CheckCaptive(ctx context.Context, probes []Probe, path PhysicalPath) (CaptiveState, string) {
	client := &http.Client{
		Timeout:   6 * time.Second,
		Transport: &http.Transport{Proxy: nil, DialContext: path.dialContext(), DisableKeepAlives: true},
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse // 重定向本身就是 Captive 的信号
		},
	}
	captive := ""
	for _, p := range probes {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.URL, nil)
		if err != nil {
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			continue
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		resp.Body.Close()
		ok := false
		if p.Expect == "" {
			ok = resp.StatusCode == http.StatusNoContent
		} else {
			ok = resp.StatusCode == http.StatusOK && strings.Contains(string(body), p.Expect)
		}
		if ok {
			return Online, p.URL
		}
		if captive == "" {
			captive = fmt.Sprintf("%s → HTTP %d %s", p.URL, resp.StatusCode, resp.Header.Get("Location"))
		}
	}
	if captive != "" {
		return Captive, captive
	}
	return Offline, "全部探测失败"
}
