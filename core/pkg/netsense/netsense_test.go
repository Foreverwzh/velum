package netsense_test

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"

	"github.com/Foreverwzh/velum/core/pkg/netsense"
)

func r(dst, iface string) netsense.Route {
	return netsense.Route{Dst: netip.MustParsePrefix(dst), Iface: iface}
}

// 合成快照：物理网卡 + 分流型企业 VPN（utun7，含作用域 DNS）+ CGNAT 网段 VPN（utun8）+ 系统 utun0（仅组播）+ Velum 自身 utun9。
func splitSnapshot() netsense.Snapshot {
	return netsense.Snapshot{
		PrimaryInterface: "en0",
		Routes: []netsense.Route{
			r("0.0.0.0/0", "en0"), r("192.168.1.0/24", "en0"),
			r("10.0.0.0/8", "utun7"), r("172.20.0.0/16", "utun7"),
			{Dst: netip.MustParsePrefix("10.8.0.5/32"), Iface: "utun7", Host: true},
			r("224.0.0.0/4", "utun7"), r("169.254.0.0/16", "utun7"),
			r("100.64.0.0/10", "utun8"),
			r("224.0.0.0/4", "utun0"),
			r("1.2.3.4/32", "utun9"), r("0.0.0.0/1", "utun9"), r("128.0.0.0/1", "utun9"),
		},
		Resolvers: []netsense.Resolver{
			{Nameservers: []string{"192.168.1.1"}, Iface: "en0"},
			{Domain: "corp.example", Nameservers: []string{"10.0.0.53"}, Iface: "utun7"},
			{Domain: "git.corp.example.", Nameservers: []string{"10.0.0.53"}, Iface: "utun7"},
			{Domain: "8.10.in-addr.arpa", Nameservers: []string{"10.0.0.53"}, Iface: "utun7"},
			{Domain: "tailnet.example", Nameservers: []string{"100.100.100.100"}, Iface: "utun8"},
			{Domain: "local", Iface: ""},
		},
	}
}

func TestAnalyzeSplitVPNs(t *testing.T) {
	a := netsense.Analyze(splitSnapshot(), "utun9")
	if a.FullTunnel() != nil {
		t.Fatal("主接口是 en0，不应判为全局型")
	}
	if len(a.VPNs) != 2 {
		t.Fatalf("应识别 utun7 / utun8，且忽略系统 utun0 与自身 utun9：%+v", a.VPNs)
	}
	corp := a.VPNs[0]
	if corp.Iface != "utun7" || len(corp.Routes) != 2 || corp.Routes[0].String() != "10.0.0.0/8" || corp.Routes[1].String() != "172.20.0.0/16" {
		t.Fatalf("utun7 网段（应排除主机 / 组播 / 链路本地）：%+v", corp)
	}
	if strings.Join(corp.Domains, ",") != "corp.example,git.corp.example" || strings.Join(corp.Nameservers, ",") != "10.0.0.53" {
		t.Fatalf("utun7 DNS（应排除反向解析域）：%+v", corp)
	}
	if ts := a.VPNs[1]; ts.Iface != "utun8" || ts.Routes[0].String() != "100.64.0.0/10" || ts.Domains[0] != "tailnet.example" {
		t.Fatalf("utun8：%+v", ts)
	}
	if len(a.Split()) != 2 {
		t.Fatal("Split()")
	}
	if strings.Join(a.SplitDNSDomains, ",") != "corp.example,git.corp.example,tailnet.example" {
		t.Fatalf("系统 DNS 分流域名：%v", a.SplitDNSDomains)
	}
}

func TestAnalyzeFullTunnel(t *testing.T) {
	// 其他代理工具 / 全局 VPN 持有默认路由且为主接口
	s := netsense.Snapshot{PrimaryInterface: "utun4", Routes: []netsense.Route{
		r("0.0.0.0/0", "utun4"), r("0.0.0.0/0", "en0"), r("10.8.32.0/20", "en0"),
		{Dst: netip.MustParsePrefix("198.18.0.1/32"), Iface: "utun4", Host: true},
	}, Resolvers: []netsense.Resolver{{Nameservers: []string{"1.0.0.1"}, Iface: "utun4"}}}
	a := netsense.Analyze(s)
	if f := a.FullTunnel(); f == nil || f.Iface != "utun4" || len(f.Routes) != 0 {
		t.Fatalf("%+v", a)
	}
	// 0/1 + 128/1 形式的全局接管
	s = netsense.Snapshot{PrimaryInterface: "utun5", Routes: []netsense.Route{r("0.0.0.0/1", "utun5"), r("128.0.0.0/1", "utun5"), r("0.0.0.0/0", "en0")}}
	if f := netsense.Analyze(s).FullTunnel(); f == nil || f.Iface != "utun5" {
		t.Fatalf("0/1+128/1 应判为全局型")
	}
	// 持有默认路由但不是主接口：不是全局型，也没有声明网段 → 忽略
	s = netsense.Snapshot{PrimaryInterface: "en0", Routes: []netsense.Route{r("0.0.0.0/0", "utun3"), r("0.0.0.0/0", "en0")}}
	if a := netsense.Analyze(s); len(a.VPNs) != 0 {
		t.Fatalf("%+v", a)
	}
}

// 代理工具的 TUN 常用一串网段（而不是 0/1 + 128/1）覆盖整个 IPv4，且不改主接口：同样是全局接管，
// 不能按分流 VPN 把几乎所有直连都送进它（该工具未退出干净时，直连会一直无响应）。
func TestAnalyzeProxyToolTakeover(t *testing.T) {
	routes := []netsense.Route{r("0.0.0.0/0", "en0"), r("10.8.32.0/20", "en0")}
	for _, d := range []string{"1.0.0.0/8", "2.0.0.0/7", "4.0.0.0/6", "8.0.0.0/5", "16.0.0.0/4", "32.0.0.0/3", "64.0.0.0/2", "128.0.0.0/1", "198.18.0.0/16"} {
		routes = append(routes, r(d, "utun6"))
	}
	s := netsense.Snapshot{PrimaryInterface: "en0", Routes: routes,
		Resolvers: []netsense.Resolver{{Nameservers: []string{"198.18.0.2"}, Iface: "en0"}}}
	a := netsense.Analyze(s)
	if f := a.FullTunnel(); f == nil || f.Iface != "utun6" || len(a.Split()) != 0 {
		t.Fatalf("应判为全局接管：%+v", a.VPNs)
	}
	if len(a.StaleDNS) != 0 {
		t.Fatalf("接管中的工具使用的虚拟 DNS 不是残留：%v", a.StaleDNS)
	}

	// 工具退出后只剩它改过的系统 DNS
	s.Routes = routes[:2]
	if a := netsense.Analyze(s); len(a.VPNs) != 0 || strings.Join(a.StaleDNS, ",") != "198.18.0.2" {
		t.Fatalf("应报告残留的虚拟 DNS：%+v", a)
	}

	// 企业 VPN 的几个大网段仍是分流
	s.Routes = append(routes[:2:2], r("10.0.0.0/8", "utun7"), r("172.16.0.0/12", "utun7"), r("100.64.0.0/10", "utun7"))
	if a := netsense.Analyze(s); a.FullTunnel() != nil || len(a.Split()) != 1 {
		t.Fatalf("应为分流：%+v", a.VPNs)
	}
}

func TestAssessmentKeyChanges(t *testing.T) {
	a := netsense.Analyze(splitSnapshot(), "utun9")
	s2 := splitSnapshot()
	s2.Routes = append(s2.Routes, r("172.21.0.0/16", "utun7"))
	if a.Key() == netsense.Analyze(s2, "utun9").Key() {
		t.Fatal("VPN 网段变化应改变 Key")
	}
	if a.Key() != netsense.Analyze(splitSnapshot(), "utun9").Key() {
		t.Fatal("相同快照 Key 应稳定")
	}
}

const scutilSample = `DNS configuration

resolver #1
  search domain[0] : corp.example
  nameserver[0] : 192.168.1.1
  if_index : 15 (en0)
  flags    : Request A records
  reach    : 0x00020002 (Reachable,Directly Reachable Address)

resolver #2
  domain   : corp.example
  nameserver[0] : 10.0.0.53
  nameserver[1] : 10.0.0.54
  if_index : 31 (utun7)
  flags    : Supplemental, Request A records
  order    : 102200

resolver #3
  domain   : local
  options  : mdns
  timeout  : 5

DNS configuration (for scoped queries)

resolver #1
  nameserver[0] : 192.168.1.1
  if_index : 15 (en0)
  flags    : Scoped, Request A records
`

func TestParseScutilDNS(t *testing.T) {
	rs := netsense.ParseScutilDNS(scutilSample)
	if len(rs) != 4 {
		t.Fatalf("%d: %+v", len(rs), rs)
	}
	if rs[1].Domain != "corp.example" || rs[1].Iface != "utun7" || strings.Join(rs[1].Nameservers, ",") != "10.0.0.53,10.0.0.54" || rs[1].Scoped {
		t.Fatalf("%+v", rs[1])
	}
	if rs[0].Domain != "" || rs[0].Iface != "en0" {
		t.Fatalf("search domain 不应当作 domain：%+v", rs[0])
	}
	if !rs[3].Scoped || rs[3].Iface != "en0" {
		t.Fatalf("%+v", rs[3])
	}
}

func TestCheckCaptive(t *testing.T) {
	online := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<HTML>Success</HTML>")) }))
	defer online.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://portal.hotel.example/login", http.StatusFound)
	}))
	defer redirect.Close()
	portal := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<form>Login</form>")) }))
	defer portal.Close()
	gen204 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) }))
	defer gen204.Close()
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	dead := "http://" + ln.Addr().String()
	ln.Close()

	ctx := context.Background()
	d := netsense.PhysicalPath{}
	cases := []struct {
		name   string
		probes []netsense.Probe
		want   netsense.CaptiveState
	}{
		{"在线", []netsense.Probe{{URL: online.URL, Expect: "Success"}}, netsense.Online},
		{"204 在线", []netsense.Probe{netsense.ParseProbe(gen204.URL)}, netsense.Online},
		{"重定向", []netsense.Probe{{URL: redirect.URL, Expect: "Success"}}, netsense.Captive},
		{"正文被替换", []netsense.Probe{{URL: portal.URL, Expect: "Success"}}, netsense.Captive},
		{"离线", []netsense.Probe{{URL: dead, Expect: "Success"}}, netsense.Offline},
		{"一个失败一个在线", []netsense.Probe{{URL: dead, Expect: "Success"}, {URL: online.URL, Expect: "Success"}}, netsense.Online},
	}
	for _, c := range cases {
		if got, why := netsense.CheckCaptive(ctx, c.probes, d); got != c.want {
			t.Errorf("%s：got %s（%s）want %s", c.name, got, why, c.want)
		}
	}
}

func TestNetworkID(t *testing.T) {
	home := netsense.Snapshot{PrimaryInterface: "en0", Routes: []netsense.Route{{Dst: netip.MustParsePrefix("0.0.0.0/0"), Iface: "en0", Gateway: "192.168.1.1"}},
		Resolvers: []netsense.Resolver{{Nameservers: []string{"192.168.1.1"}, Iface: "en0"}}}
	hotel := home
	hotel.Routes = []netsense.Route{{Dst: netip.MustParsePrefix("0.0.0.0/0"), Iface: "en0", Gateway: "10.20.0.1"}}
	if home.NetworkID() == hotel.NetworkID() {
		t.Fatal("同一网卡换网络（网关变化）应视为网络变化")
	}
	withVPN := home
	withVPN.Routes = append(append([]netsense.Route(nil), home.Routes...), netsense.Route{Dst: netip.MustParsePrefix("0.0.0.0/0"), Iface: "utun4"})
	if home.NetworkID() != withVPN.NetworkID() {
		t.Fatal("隧道接口的默认路由不应改变网络身份")
	}
}

func TestPhysicalPathHelpers(t *testing.T) {
	rs := netsense.ParseScutilDNS(scutilSample)
	s := netsense.Snapshot{PrimaryInterface: "utun4", Resolvers: rs, Routes: []netsense.Route{
		{Dst: netip.MustParsePrefix("0.0.0.0/0"), Iface: "utun4"}, {Dst: netip.MustParsePrefix("0.0.0.0/0"), Iface: "en0", Gateway: "192.168.1.1"}}}
	if s.PhysicalInterface() != "en0" || strings.Join(s.PhysicalNameservers("en0"), ",") != "192.168.1.1" {
		t.Fatalf("%s %v", s.PhysicalInterface(), s.PhysicalNameservers("en0"))
	}
}

// /etc/resolver 声明的域名（不带接口）也属于系统 DNS 分流。
func TestSplitDNSFromEtcResolver(t *testing.T) {
	s := netsense.Snapshot{PrimaryInterface: "en0", Resolvers: []netsense.Resolver{
		{Nameservers: []string{"192.168.1.1"}, Iface: "en0"},
		{Domain: "corp.example", Nameservers: []string{"127.0.0.1", "10.0.0.53"}},
		{Domain: "local"},
		{Domain: "mdns-only.example"},
	}}
	a := netsense.Analyze(s)
	if len(a.VPNs) != 0 || strings.Join(a.SplitDNSDomains, ",") != "corp.example" {
		t.Fatalf("%+v", a)
	}
}

func TestPhysicalDNS(t *testing.T) {
	ns := netsense.ParseDHCPNameserversForTest("op = BOOTREPLY\ndomain_name_server (ip_mult): {10.0.0.1, 10.0.0.2}\nend (none):\n")
	if strings.Join(ns, ",") != "10.0.0.1,10.0.0.2" {
		t.Fatalf("%v", ns)
	}
	// Velum 接管系统 DNS 后，作用域 DNS 变为 TUN 地址：应优先 DHCP 值，并剔除 Velum 自己的地址
	s := netsense.Snapshot{Routes: []netsense.Route{{Dst: netip.MustParsePrefix("0.0.0.0/0"), Iface: "en0"}},
		Resolvers: []netsense.Resolver{{Scoped: true, Iface: "en0", Nameservers: []string{"172.19.0.2"}}}}
	if got := s.PhysicalDNS("172.19.0.2"); len(got) != 0 {
		t.Fatalf("应剔除 Velum 自己的 DNS：%v", got)
	}
	s.DHCPNameservers = []string{"10.0.0.1"}
	if got := s.PhysicalDNS("172.19.0.2"); strings.Join(got, ",") != "10.0.0.1" {
		t.Fatalf("%v", got)
	}
}
