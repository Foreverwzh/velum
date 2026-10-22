package netsense

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"sort"
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
		if ip := net.ParseIP(host); ip != nil {
			return d.DialContext(ctx, network, addr)
		}
		ips, err := r.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("解析 %s 无结果", host)
		}
		// 依次尝试各个地址（IPv4 优先）：只试第一个时，它不通（如所在网络的 IPv6 不可用）就会误判为离线
		sort.SliceStable(ips, func(i, j int) bool { return ips[i].IP.To4() != nil && ips[j].IP.To4() == nil })
		for _, ip := range ips {
			var c net.Conn
			if c, err = d.DialContext(ctx, network, net.JoinHostPort(ip.IP.String(), port)); err == nil {
				return c, nil
			}
		}
		return nil, err
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
	var errs []string
	for _, p := range probes {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, p.URL, nil)
		if err != nil {
			continue
		}
		resp, err := client.Do(req)
		if err != nil {
			errs = append(errs, probeErr(req.URL.Host, err))
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
	if len(errs) == 0 {
		return Offline, "全部探测失败"
	}
	return Offline, "全部探测失败：" + strings.Join(errs, "；")
}

// probeErr 把探测错误写成简短的一句（去掉 url.Error 中重复的方法与地址）。
func probeErr(host string, err error) string {
	var ue *url.Error
	if errors.As(err, &ue) {
		err = ue.Err
	}
	return host + " " + err.Error()
}
