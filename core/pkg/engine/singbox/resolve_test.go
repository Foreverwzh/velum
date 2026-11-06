package singbox_test

import (
	"context"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Foreverwzh/velum/core/pkg/compiler"
	"github.com/Foreverwzh/velum/core/pkg/engine"
	"github.com/Foreverwzh/velum/core/pkg/engine/singbox"
	"github.com/Foreverwzh/velum/core/pkg/engine/singbox/render"
	"github.com/Foreverwzh/velum/core/pkg/overlay"
	"github.com/Foreverwzh/velum/core/pkg/subscription"
)

// fakeDNS 按域名前缀应答：nx 返回 NXDOMAIN，sf 返回 SERVFAIL，to 不应答，其余返回 answers 里的地址。
func fakeDNS(t *testing.T, answers map[string]string) int {
	t.Helper()
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
	return pc.LocalAddr().(*net.UDPAddr).Port
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
