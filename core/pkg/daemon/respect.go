package daemon

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"strings"
	"time"

	"github.com/Foreverwzh/velum/core/pkg/explain"
	"github.com/Foreverwzh/velum/core/pkg/ir"
	"github.com/Foreverwzh/velum/core/pkg/netsense"
)

// dnsDefaultPort 是各类型 DNS 服务器的默认端口（推演时作为目标端口）。
var dnsDefaultPort = map[string]uint16{"udp": 53, "tcp": 53, "tls": 853, "quic": 853, "https": 443, "h3": 443}

const dnsHostTTL = time.Hour

type dnsHostEntry struct {
	addrs []netip.Addr
	at    time.Time
}

// applyRespectRules 实现 Clash 的 dns.respect-rules：用静态推演（与 vlu why 相同）求出连接每个 DNS 服务器时命中的出站，
// 写入其 Detour。域名形式的服务器先用 default-nameserver 解析，以便匹配 IP 类规则（与 mihomo 一致）。
// 命中 DIRECT（或 VPN 直连）时保持直连；命中拦截时保持直连并告警（拦截会让 DNS 整体失效）。
func (d *Daemon) applyRespectRules(st *state) {
	servers := st.plan.IR.DNS.Servers
	need := false
	for _, s := range servers {
		need = need || (s.RespectRules && s.Detour == "")
	}
	if !need {
		return
	}
	loaded, _ := d.loadRuleSets(st)
	var notes []string
	for i := range servers {
		sv := &servers[i]
		if !sv.RespectRules || sv.Detour != "" {
			continue
		}
		t := explain.Target{Port: uint16(sv.Port), Network: "tcp"}
		if t.Port == 0 {
			t.Port = dnsDefaultPort[sv.Type]
		}
		if sv.Type == "udp" || sv.Type == "quic" {
			t.Network = "udp"
		}
		if ip, err := netip.ParseAddr(sv.Address); err == nil {
			t.IP = ip
		} else {
			t.Domain = sv.Address
			t.ResolvedIPs = d.bootstrapResolve(sv.Address, servers, st.plan.IR.DNS.FakeIP)
		}
		dec := explain.WhyWith(st.plan, t, loaded).Decision
		switch {
		case dec.Action == ir.ActionReject || dec.Action == ir.ActionDrop:
			st.plan.Warnings = append(st.plan.Warnings, ir.Warning{Category: "dns", Kind: "respect-rules", Level: ir.Degraded,
				Reason: "该 DNS 服务器命中拦截规则，保持直连（拦截会让 DNS 解析失效）", Subject: sv.Address, Source: dec.Source})
		case dec.Action != ir.ActionRoute || dec.Outbound == "" || dec.Outbound == ir.OutboundDirect:
			// 直连：保持默认
		default:
			sv.Detour = dec.Outbound
			notes = append(notes, fmt.Sprintf("%s → %s", sv.Address, dec.Outbound))
		}
	}
	if len(notes) > 0 {
		d.Logger.Printf("DNS 按规则选择出站（respect-rules）：%s", strings.Join(notes, "，"))
	}
}

// bootstrapResolve 用 default-nameserver（IR 中的 bootstrap 服务器）解析 DNS 服务器的域名，结果缓存一小时。
// 失败时返回空：推演中依赖 IP 的规则记为未知，结论按其余规则得出。
//
// 查询绑定物理网卡发出：TUN 开启时不经 TUN（否则会被 DNS 劫持拿到 fake-ip）；落在 fake-ip 网段的结果一律丢弃。
func (d *Daemon) bootstrapResolve(host string, servers []ir.DNSServer, fake *ir.FakeIP) []netip.Addr {
	d.mu.Lock()
	if e, ok := d.dnsHosts[host]; ok && time.Since(e.at) < dnsHostTTL {
		d.mu.Unlock()
		return e.addrs
	}
	d.mu.Unlock()
	var boots []string
	for _, s := range servers {
		if strings.HasPrefix(s.Tag, "bootstrap-") && s.Type == "udp" {
			port := s.Port
			if port == 0 {
				port = 53
			}
			boots = append(boots, net.JoinHostPort(s.Address, fmt.Sprint(port)))
		}
	}
	if len(boots) == 0 {
		return nil
	}
	d.mu.RLock()
	phys := d.env.PrimaryInterface
	d.mu.RUnlock()
	dialer := &net.Dialer{}
	if ifi, err := net.InterfaceByName(phys); err == nil && phys != "" {
		dialer = netsense.BoundDialer(ifi.Index)
	}
	var fakeRange netip.Prefix
	if fake != nil {
		fakeRange, _ = netip.ParsePrefix(fake.Inet4Range)
	}
	var addrs []netip.Addr
	for _, b := range boots {
		r := &net.Resolver{PreferGo: true, Dial: func(ctx context.Context, network, _ string) (net.Conn, error) {
			return dialer.DialContext(ctx, network, b)
		}}
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		ips, err := r.LookupNetIP(ctx, "ip4", host)
		cancel()
		for _, ip := range ips {
			if ip = ip.Unmap(); err == nil && !(fakeRange.IsValid() && fakeRange.Contains(ip)) {
				addrs = append(addrs, ip)
			}
		}
		if len(addrs) > 0 {
			break
		}
	}
	if len(addrs) > 0 {
		d.mu.Lock()
		if d.dnsHosts == nil {
			d.dnsHosts = map[string]dnsHostEntry{}
		}
		d.dnsHosts[host] = dnsHostEntry{addrs: addrs, at: time.Now()}
		d.mu.Unlock()
	}
	return addrs
}
