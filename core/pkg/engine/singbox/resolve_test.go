package singbox_test

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Foreverwzh/velum/core/pkg/compiler"
	"github.com/Foreverwzh/velum/core/pkg/engine"
	"github.com/Foreverwzh/velum/core/pkg/engine/singbox"
	"github.com/Foreverwzh/velum/core/pkg/engine/singbox/render"
	"github.com/Foreverwzh/velum/core/pkg/overlay"
	"github.com/Foreverwzh/velum/core/pkg/subscription"

	mDNS "github.com/miekg/dns"
)

// fakeDNS 按域名前缀应答：nx 返回 NXDOMAIN，sf 返回 SERVFAIL，to 不应答，其余返回 answers 里的地址。
func fakeDNS(t *testing.T, answers map[string]string) int {
	t.Helper()
	port, _ := fakeDNSLog(t, answers)
	return port
}

// fakeDNSLog 同 fakeDNS，并返回到目前为止查询过的域名。
func fakeDNSLog(t *testing.T, answers map[string]string) (int, func() []string) {
	t.Helper()
	port, asked, _ := fakeDNSCtl(t, answers)
	return port, asked
}

// fakeDNSCtl 同 fakeDNSLog，另返回停止服务的函数（之后的查询都没有应答）。
func fakeDNSCtl(t *testing.T, answers map[string]string) (int, func() []string, func()) {
	t.Helper()
	var down atomic.Bool
	var mu sync.Mutex
	var asked []string
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { pc.Close() })
	go func() {
		buf := make([]byte, 2048)
		for {
			n, addr, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			m := buf[:n]
			i, labels := 12, []string{}
			for m[i] != 0 {
				labels = append(labels, string(m[i+1:i+1+int(m[i])]))
				i += int(m[i]) + 1
			}
			name := strings.Join(labels, ".")
			if down.Load() {
				continue
			}
			mu.Lock()
			asked = append(asked, name)
			mu.Unlock()
			qtype := binary.BigEndian.Uint16(m[i+1:])
			question := m[12 : i+5]
			rcode, answer := uint16(0), []byte(nil)
			switch {
			case strings.HasPrefix(name, "to."):
				continue
			case strings.HasPrefix(name, "nx."):
				rcode = 3
			case strings.HasPrefix(name, "sf."):
				rcode = 2
			case qtype == 1:
				ip := net.ParseIP(answers[name]).To4()
				answer = append([]byte{0xc0, 0x0c, 0, 1, 0, 1, 0, 0, 0, 1, 0, 4}, ip...)
			}
			count := uint16(0)
			if answer != nil {
				count = 1
			}
			resp := append([]byte{}, m[:2]...)
			resp = binary.BigEndian.AppendUint16(resp, 0x8180|rcode)
			resp = binary.BigEndian.AppendUint16(resp, 1)
			resp = binary.BigEndian.AppendUint16(resp, count)
			resp = append(resp, 0, 0, 0, 0)
			resp = append(resp, question...)
			resp = append(resp, answer...)
			_, _ = pc.WriteTo(resp, addr)
		}
	}()
	return pc.LocalAddr().(*net.UDPAddr).Port, func() []string {
		mu.Lock()
		defer mu.Unlock()
		return slices.Clone(asked)
	}, func() { down.Store(true) }
}

// fakeNode 是一个 SOCKS5 节点：记录收到的目标，并以 200 应答任何请求。
type fakeNode struct {
	port int
	mu   sync.Mutex
	seen []string
}

func (f *fakeNode) targets() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.seen)
}

func startFakeNode(t *testing.T) *fakeNode {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	f := &fakeNode{port: ln.Addr().(*net.TCPAddr).Port}
	serve := func(c net.Conn) {
		defer c.Close()
		head := make([]byte, 2)
		if _, err := io.ReadFull(c, head); err != nil {
			return
		}
		if _, err := io.CopyN(io.Discard, c, int64(head[1])); err != nil {
			return
		}
		_, _ = c.Write([]byte{5, 0})
		req := make([]byte, 4)
		if _, err := io.ReadFull(c, req); err != nil {
			return
		}
		var dst string
		switch req[3] {
		case 1:
			b := make([]byte, 4)
			_, _ = io.ReadFull(c, b)
			dst = net.IP(b).String()
		case 3:
			l := make([]byte, 1)
			_, _ = io.ReadFull(c, l)
			b := make([]byte, l[0])
			_, _ = io.ReadFull(c, b)
			dst = string(b)
		default:
			return
		}
		_, _ = io.CopyN(io.Discard, c, 2)
		f.mu.Lock()
		f.seen = append(f.seen, dst)
		f.mu.Unlock()
		_, _ = c.Write([]byte{5, 0, 0, 1, 0, 0, 0, 0, 0, 0})
		_, _ = c.Read(make([]byte, 4096))
		_, _ = c.Write([]byte("HTTP/1.1 200 OK\r\nContent-Length: 4\r\nConnection: close\r\n\r\nnode"))
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go serve(c)
		}
	}()
	return f
}

// 解析域名只为匹配 IP 规则：查不到不能断开连接，走代理时要把域名交给节点（本地结果可能被污染，或是离本机而不是离节点近的 CDN）；
// 走直连时才使用解析结果。
func TestResolveOnlyServesIPRules(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("target")) }))
	defer target.Close()
	_, targetPort, _ := net.SplitHostPort(target.Listener.Addr().String())

	dns := fakeDNS(t, map[string]string{
		"poisoned.test": "203.0.113.9", "blockedip.test": "10.99.0.1",
		"direct.test": "127.0.0.1", "viagroup.test": "127.0.0.1",
	})
	node := startFakeNode(t)
	sub, err := subscription.Parse([]byte(fmt.Sprintf(`
proxies:
  - {name: node, type: socks5, server: 127.0.0.1, port: %d}
proxy-groups:
  - {name: PROXY, type: select, proxies: [node]}
  - {name: G, type: select, proxies: [DIRECT]}
dns:
  enable: true
  ipv6: false
  nameserver: [udp://127.0.0.1:%d]
rules:
  - IP-CIDR,10.99.0.0/16,REJECT
  - DOMAIN,direct.test,DIRECT
  - DOMAIN,viagroup.test,G
  - MATCH,PROXY
`, node.port, dns)), subscription.Options{File: "sub.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	listen := freePort(t)
	plan, err := compiler.Compile(sub, &overlay.Overlay{}, compiler.Options{Mode: compiler.ModeProxy, Listen: listen})
	if err != nil {
		t.Fatal(err)
	}
	r, err := render.Render(plan, render.Options{})
	if err != nil {
		t.Fatal(err)
	}
	eng := singbox.New(singbox.Options{})
	if err := eng.Start(context.Background(), engine.Config{JSON: r.JSON, RuleSources: r.RuleSources, RuleIndex: r.RuleIndex}); err != nil {
		t.Fatal(err)
	}
	defer eng.Stop()

	proxyURL, _ := url.Parse("http://" + listen.String())
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL), DisableKeepAlives: true}, Timeout: 8 * time.Second}
	get := func(host string) (int, string) {
		resp, err := client.Get("http://" + host + ":" + targetPort + "/")
		if err != nil {
			return 0, ""
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(body)
	}

	for _, host := range []string{"poisoned.test", "nx.test", "sf.test", "to.test"} {
		start := time.Now()
		code, body := get(host)
		if code != 200 || body != "node" {
			t.Errorf("%s 应当经节点返回：%d %q", host, code, body)
		}
		if !slices.Contains(node.targets(), host) {
			t.Errorf("%s：节点应当收到域名，实际 %v", host, node.targets())
		}
		if host == "to.test" && time.Since(start) > 5*time.Second {
			t.Errorf("DNS 不应答时应在 3 秒左右放弃，实际 %v", time.Since(start))
		}
	}
	// 走直连（包括经策略组选直连）用解析结果：节点不该收到这些连接
	for _, host := range []string{"direct.test", "viagroup.test"} {
		if code, body := get(host); code != 200 || body != "target" {
			t.Errorf("%s 应当直连到目标：%d %q", host, code, body)
		}
	}
	// IP 规则仍要按解析结果匹配
	if code, body := get("blockedip.test"); body == "node" || code == 200 {
		t.Errorf("blockedip.test 应当命中 IP 规则被拒绝：%d %q", code, body)
	}
	for _, seen := range node.targets() {
		if seen == "direct.test" || seen == "viagroup.test" || seen == "blockedip.test" || net.ParseIP(seen) != nil {
			t.Errorf("节点不该收到 %s（已收到 %v）", seen, node.targets())
		}
	}
}

// domain_first：命中域名规则的连接不查 DNS，且不受排在前面的 IP 规则影响；没命中才解析，IP 规则仍然有效。
func TestDomainFirstSkipsDNS(t *testing.T) {
	port, asked := fakeDNSLog(t, map[string]string{"listed.test": "10.99.0.1", "unlisted.test": "10.99.0.1", "other.test": "203.0.113.9"})
	node := startFakeNode(t)
	sub, err := subscription.Parse([]byte(fmt.Sprintf(`
proxies:
  - {name: node, type: socks5, server: 127.0.0.1, port: %d}
proxy-groups:
  - {name: PROXY, type: select, proxies: [node]}
dns:
  enable: true
  ipv6: false
  nameserver: [udp://127.0.0.1:%d]
rules:
  - IP-CIDR,10.99.0.0/16,REJECT
  - DOMAIN,listed.test,PROXY
  - MATCH,PROXY
`, node.port, port)), subscription.Options{File: "sub.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	listen := freePort(t)
	plan, err := compiler.Compile(sub, &overlay.Overlay{}, compiler.Options{Mode: compiler.ModeProxy, Listen: listen, DomainFirst: true})
	if err != nil {
		t.Fatal(err)
	}
	r, err := render.Render(plan, render.Options{})
	if err != nil {
		t.Fatal(err)
	}
	eng := singbox.New(singbox.Options{})
	if err := eng.Start(context.Background(), engine.Config{JSON: r.JSON, RuleSources: r.RuleSources, RuleIndex: r.RuleIndex}); err != nil {
		t.Fatal(err)
	}
	defer eng.Stop()
	proxyURL, _ := url.Parse("http://" + listen.String())
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL), DisableKeepAlives: true}, Timeout: 8 * time.Second}
	get := func(host string) string {
		resp, err := client.Get("http://" + host + "/")
		if err != nil {
			return ""
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return string(body)
	}
	// listed.test 解析出来会命中前面的 IP 规则，但域名规则优先
	if body := get("listed.test"); body != "node" {
		t.Errorf("listed.test 应当命中域名规则走节点：%q", body)
	}
	if slices.Contains(asked(), "listed.test") {
		t.Errorf("命中域名规则不该查 DNS：%v", asked())
	}
	if body := get("other.test"); body != "node" {
		t.Errorf("other.test 应当解析后落到 MATCH：%q", body)
	}
	if !slices.Contains(asked(), "other.test") {
		t.Errorf("没命中域名规则时应当解析：%v", asked())
	}
	if body := get("unlisted.test"); body == "node" {
		t.Error("unlisted.test 解析后应当命中 IP 规则被拒绝")
	}
}

// 订阅的 DNS 连不上（如 DoH 被所在网络拒绝）：内核改问系统 DNS，直连域名照常解析。
func TestSubscriptionDNSFallsBackToSystem(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("target")) }))
	defer target.Close()
	_, targetPort, _ := net.SplitHostPort(target.Listener.Addr().String())
	system := fakeDNS(t, map[string]string{"direct.test": "127.0.0.1"})
	dead := freePort(t) // 没有服务在听：查询立即被拒绝
	sub, err := subscription.Parse([]byte(fmt.Sprintf(`
dns:
  enable: true
  ipv6: false
  nameserver: [tcp://127.0.0.1:%d]
rules:
  - MATCH,DIRECT
`, dead.Port())), subscription.Options{File: "sub.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	listen := freePort(t)
	plan, err := compiler.Compile(sub, &overlay.Overlay{}, compiler.Options{Mode: compiler.ModeProxy, Listen: listen})
	if err != nil {
		t.Fatal(err)
	}
	for i := range plan.IR.DNS.Servers { // 用假的系统 DNS 代替本机真实的系统解析器
		if sv := &plan.IR.DNS.Servers[i]; sv.Tag == compiler.LocalDNS {
			sv.Type, sv.Address, sv.Port = "udp", "127.0.0.1", system
		}
	}
	r, err := render.Render(plan, render.Options{})
	if err != nil {
		t.Fatal(err)
	}
	eng := singbox.New(singbox.Options{})
	if err := eng.Start(context.Background(), engine.Config{JSON: r.JSON, RuleSources: r.RuleSources, RuleIndex: r.RuleIndex}); err != nil {
		t.Fatal(err)
	}
	defer eng.Stop()
	proxyURL, _ := url.Parse("http://" + listen.String())
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL), DisableKeepAlives: true}, Timeout: 8 * time.Second}
	start := time.Now()
	resp, err := client.Get("http://direct.test:" + targetPort + "/")
	if err != nil {
		t.Fatalf("订阅的 DNS 不可用时应改用系统 DNS：%v", err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(body) != "target" {
		t.Fatalf("body=%q", body)
	}
	if d := time.Since(start); d > 2*time.Second {
		t.Errorf("用时 %v：主上游被拒绝后应立即改问系统 DNS", d)
	}
}

// 物理网络的 DNS 在运行中更新：之后的查询改问新地址（含之前缓存过的域名），不重启内核。
func TestPhysicalDNSUpdatesWithoutRestart(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("target")) }))
	defer target.Close()
	_, targetPort, _ := net.SplitHostPort(target.Listener.Addr().String())
	home, askedHome := fakeDNSLog(t, map[string]string{"x.test": "127.0.0.1"})
	office, askedOffice := fakeDNSLog(t, map[string]string{"x.test": "127.0.0.1", "y.test": "127.0.0.1"})
	sub, err := subscription.Parse([]byte("dns:\n  enable: true\n  ipv6: false\n  nameserver: [system]\nrules:\n  - MATCH,DIRECT\n"), subscription.Options{File: "sub.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	listen := freePort(t)
	plan, err := compiler.Compile(sub, &overlay.Overlay{}, compiler.Options{Mode: compiler.ModeProxy, Listen: listen, Platform: "android",
		Env: &compiler.Environment{PhysicalDNS: []string{"127.0.0.1"}}})
	if err != nil {
		t.Fatal(err)
	}
	r, err := render.Render(plan, render.Options{})
	if err != nil {
		t.Fatal(err)
	}
	eng := singbox.New(singbox.Options{})
	eng.SetPhysicalDNS([]string{fmt.Sprintf("127.0.0.1:%d", home)})
	if err := eng.Start(context.Background(), engine.Config{JSON: r.JSON, RuleSources: r.RuleSources, RuleIndex: r.RuleIndex}); err != nil {
		t.Fatal(err)
	}
	defer eng.Stop()
	proxyURL, _ := url.Parse("http://" + listen.String())
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL), DisableKeepAlives: true}, Timeout: 5 * time.Second}
	get := func(host string) string {
		resp, err := client.Get("http://" + host + ":" + targetPort + "/")
		if err != nil {
			return err.Error()
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return string(body)
	}
	if b := get("x.test"); b != "target" || !slices.Contains(askedHome(), "x.test") {
		t.Fatalf("x.test 应经家里的 DNS 解析：%q asked=%v", b, askedHome())
	}
	eng.SetPhysicalDNS([]string{fmt.Sprintf("127.0.0.1:%d", office)})
	for _, h := range []string{"x.test", "y.test"} {
		if b := get(h); b != "target" || !slices.Contains(askedOffice(), h) {
			t.Errorf("换网后 %s 应改问新网络的 DNS：%q asked=%v", h, b, askedOffice())
		}
	}
	if slices.Contains(askedHome(), "y.test") {
		t.Errorf("换网后不应再问旧网络的 DNS：%v", askedHome())
	}
	// 网络下发多个 DNS、其中一个不应答：并发询问，不等它超时
	silent, _ := net.ListenPacket("udp4", "127.0.0.1:0")
	defer silent.Close()
	eng.SetPhysicalDNS([]string{silent.LocalAddr().String(), fmt.Sprintf("127.0.0.1:%d", office)})
	start := time.Now()
	if b := get("x.test"); b != "target" {
		t.Errorf("一个物理 DNS 不应答时应由另一个应答：%q", b)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("用时 %v：应并发询问各个物理 DNS", d)
	}
}

// 上游 DNS 全部不可用：缓存过期的域名仍用旧应答（serve-stale），不会解析失败。
func TestStaleAnswerWhenUpstreamDown(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("target")) }))
	defer target.Close()
	_, targetPort, _ := net.SplitHostPort(target.Listener.Addr().String())
	port, _, stop := fakeDNSCtl(t, map[string]string{"x.test": "127.0.0.1"}) // 应答的 TTL 为 1 秒
	sub, err := subscription.Parse([]byte("dns:\n  enable: true\n  ipv6: false\n  nameserver: [system]\nrules:\n  - MATCH,DIRECT\n"), subscription.Options{File: "sub.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	listen := freePort(t)
	plan, err := compiler.Compile(sub, &overlay.Overlay{}, compiler.Options{Mode: compiler.ModeProxy, Listen: listen, Platform: "android",
		Env: &compiler.Environment{PhysicalDNS: []string{"127.0.0.1"}}})
	if err != nil {
		t.Fatal(err)
	}
	r, err := render.Render(plan, render.Options{})
	if err != nil {
		t.Fatal(err)
	}
	eng := singbox.New(singbox.Options{})
	eng.SetPhysicalDNS([]string{fmt.Sprintf("127.0.0.1:%d", port)})
	if err := eng.Start(context.Background(), engine.Config{JSON: r.JSON, RuleSources: r.RuleSources, RuleIndex: r.RuleIndex}); err != nil {
		t.Fatal(err)
	}
	defer eng.Stop()
	proxyURL, _ := url.Parse("http://" + listen.String())
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL), DisableKeepAlives: true}, Timeout: 5 * time.Second}
	get := func() string {
		resp, err := client.Get("http://x.test:" + targetPort + "/")
		if err != nil {
			return err.Error()
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return string(body)
	}
	if b := get(); b != "target" {
		t.Fatalf("首次解析：%q", b)
	}
	stop()
	time.Sleep(1500 * time.Millisecond) // 缓存过期
	start := time.Now()
	if b := get(); b != "target" {
		t.Fatalf("上游不可用时应使用过期的缓存应答：%q", b)
	}
	if d := time.Since(start); d > time.Second {
		t.Errorf("用时 %v：过期应答应立即返回，后台再刷新", d)
	}
}

// 走 fake-ip 的域名：HTTPS / SVCB 查询直接回空应答、不发往上游；fake-ip-filter 里的域名照常转发；allow_dns_svcb 打开后都转发。
// TUN 需要权限：用 TUN 模式编译后，把 TUN 入站换成本机 UDP 入站（之后同样经嗅探与 hijack-dns 处理）。
func TestFakeIPBlocksSVCB(t *testing.T) {
	for _, allow := range []bool{false, true} {
		upstream, asked := fakeDNSLog(t, map[string]string{})
		sub, err := subscription.Parse([]byte(fmt.Sprintf(`
dns:
  enable: true
  ipv6: false
  enhanced-mode: fake-ip
  fake-ip-filter: ["+.filtered.test"]
  nameserver: [udp://127.0.0.1:%d]
rules:
  - MATCH,DIRECT
`, upstream)), subscription.Options{File: "sub.yaml"})
		if err != nil {
			t.Fatal(err)
		}
		plan, err := compiler.Compile(sub, &overlay.Overlay{}, compiler.Options{Mode: compiler.ModeTUN, Listen: freePort(t),
			TUN: compiler.TUNOptions{Address: netip.MustParsePrefix("172.19.0.1/30"), AllowSVCB: allow}})
		if err != nil {
			t.Fatal(err)
		}
		r, err := render.Render(plan, render.Options{})
		if err != nil {
			t.Fatal(err)
		}
		var cfg map[string]any
		if err := json.Unmarshal(r.JSON, &cfg); err != nil {
			t.Fatal(err)
		}
		dnsIn := freePort(t)
		var inbounds []any
		for _, in := range cfg["inbounds"].([]any) {
			if in.(map[string]any)["type"] != "tun" {
				inbounds = append(inbounds, in)
			}
		}
		cfg["inbounds"] = append(inbounds, map[string]any{"type": "direct", "tag": "dns-in", "listen": "127.0.0.1", "listen_port": dnsIn.Port(), "network": "udp"})
		js, _ := json.Marshal(cfg)
		eng := singbox.New(singbox.Options{})
		if err := eng.Start(context.Background(), engine.Config{JSON: js, RuleSources: r.RuleSources, RuleIndex: r.RuleIndex}); err != nil {
			t.Fatal(err)
		}
		query := func(name string, qtype uint16) *mDNS.Msg {
			m := new(mDNS.Msg)
			m.SetQuestion(name+".", qtype)
			resp, err := mDNS.Exchange(m, dnsIn.String())
			if err != nil {
				t.Fatalf("%s %s：%v", name, mDNS.TypeToString[qtype], err)
			}
			return resp
		}
		for _, qt := range []uint16{mDNS.TypeHTTPS, mDNS.TypeSVCB} {
			resp := query("www.proxied.test", qt)
			blocked := !slices.Contains(asked(), "www.proxied.test")
			switch {
			case !allow && (!blocked || resp.Rcode != mDNS.RcodeSuccess || len(resp.Answer) != 0):
				t.Errorf("走 fake-ip 的域名查 %s 应直接回空应答、不发往上游：rcode=%d answer=%v asked=%v", mDNS.TypeToString[qt], resp.Rcode, resp.Answer, asked())
			case allow && blocked:
				t.Errorf("allow_dns_svcb 打开后 %s 查询应发往上游", mDNS.TypeToString[qt])
			}
		}
		if a := query("www.proxied.test", mDNS.TypeA); len(a.Answer) != 1 || !strings.HasPrefix(a.Answer[0].(*mDNS.A).A.String(), "198.18.") {
			t.Errorf("A 记录应为 fake-ip：%v", a.Answer)
		}
		query("www.filtered.test", mDNS.TypeHTTPS)
		if !slices.Contains(asked(), "www.filtered.test") {
			t.Errorf("fake-ip-filter 里的域名应照常查询 HTTPS 记录：asked=%v", asked())
		}
		eng.Stop()
	}
}

// 内核重启（换配置、重新加载）后，重启前发出的 fake-ip 仍能找回域名：应用会缓存拿到的 fake-ip，
// 否则在它们过期前，这些连接都会失败（missing fakeip record）。
func TestFakeIPSurvivesRestart(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("target")) }))
	defer target.Close()
	_, targetPort, _ := net.SplitHostPort(target.Listener.Addr().String())
	upstream := fakeDNS(t, map[string]string{"x.test": "127.0.0.1"})
	sub, err := subscription.Parse([]byte(fmt.Sprintf(`
dns:
  enable: true
  ipv6: false
  enhanced-mode: fake-ip
  nameserver: [udp://127.0.0.1:%d]
rules:
  - MATCH,DIRECT
`, upstream)), subscription.Options{File: "sub.yaml"})
	if err != nil {
		t.Fatal(err)
	}
	listen := freePort(t)
	plan, err := compiler.Compile(sub, &overlay.Overlay{}, compiler.Options{Mode: compiler.ModeTUN, Listen: listen,
		TUN: compiler.TUNOptions{Address: netip.MustParsePrefix("172.19.0.1/30")}})
	if err != nil {
		t.Fatal(err)
	}
	r, err := render.Render(plan, render.Options{})
	if err != nil {
		t.Fatal(err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(r.JSON, &cfg); err != nil {
		t.Fatal(err)
	}
	dnsIn := freePort(t)
	var inbounds []any
	for _, in := range cfg["inbounds"].([]any) {
		if in.(map[string]any)["type"] != "tun" {
			inbounds = append(inbounds, in)
		}
	}
	cfg["inbounds"] = append(inbounds, map[string]any{"type": "direct", "tag": "dns-in", "listen": "127.0.0.1", "listen_port": dnsIn.Port(), "network": "udp"})
	js, _ := json.Marshal(cfg)
	ecfg := engine.Config{JSON: js, RuleSources: r.RuleSources, RuleIndex: r.RuleIndex, CachePath: filepath.Join(t.TempDir(), "singbox.db")}
	eng := singbox.New(singbox.Options{})
	if err := eng.Start(context.Background(), ecfg); err != nil {
		t.Fatal(err)
	}
	defer eng.Stop()
	m := new(mDNS.Msg)
	m.SetQuestion("x.test.", mDNS.TypeA)
	resp, err := mDNS.Exchange(m, dnsIn.String())
	if err != nil || len(resp.Answer) != 1 {
		t.Fatalf("fake-ip 查询：%v %v", resp, err)
	}
	fake := resp.Answer[0].(*mDNS.A).A.String()
	if !strings.HasPrefix(fake, "198.18.") {
		t.Fatalf("应拿到 fake-ip：%s", fake)
	}
	if err := eng.Restart(context.Background(), ecfg); err != nil {
		t.Fatal(err)
	}
	proxyURL, _ := url.Parse("http://" + listen.String())
	client := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL), DisableKeepAlives: true}, Timeout: 5 * time.Second}
	res, err := client.Get("http://" + fake + ":" + targetPort + "/")
	if err != nil {
		t.Fatalf("重启后用之前的 fake-ip %s 连接应找回 x.test：%v", fake, err)
	}
	body, _ := io.ReadAll(res.Body)
	res.Body.Close()
	if string(body) != "target" {
		t.Fatalf("body=%q", body)
	}
	// 同一域名重启后仍分到同一个 fake-ip
	if resp, err = mDNS.Exchange(m, dnsIn.String()); err != nil || resp.Answer[0].(*mDNS.A).A.String() != fake {
		t.Errorf("重启后 x.test 应仍是 %s：%v %v", fake, resp, err)
	}
}
