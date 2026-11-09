package subscription_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/Foreverwzh/velum/core/pkg/ir"
	"github.com/Foreverwzh/velum/core/pkg/subscription"
)

const singBoxSub = `{
  "log": {"level": "info"},
  "inbounds": [{"type": "tun", "tag": "tun-in", "address": ["172.19.0.1/30", "fdfe::1/126"]}],
  "dns": {
    "servers": [
      {"tag": "local", "type": "local"},
      {"tag": "ali", "type": "udp", "server": "223.5.5.5"},
      {"tag": "cf", "type": "https", "server": "cloudflare-dns.com", "path": "/dns-query", "detour": "Proxy", "domain_resolver": "ali"},
      {"tag": "fake", "type": "fakeip", "inet4_range": "198.18.0.0/15"},
      {"tag": "old", "address": "tls://8.8.8.8"}
    ],
    "rules": [
      {"domain_suffix": ["corp.example"], "server": "local"},
      {"rule_set": ["cn"], "server": "ali"},
      {"query_type": ["A", "AAAA"], "server": "fake"},
      {"clash_mode": "Direct", "server": "ali"}
    ],
    "final": "cf",
    "strategy": "prefer_ipv4",
    "reverse_mapping": true
  },
  "outbounds": [
    {"type": "selector", "tag": "Proxy", "outbounds": ["Auto", "hk", "direct", "gone"], "default": "Auto"},
    {"type": "urltest", "tag": "Auto", "outbounds": ["hk", "jp"], "url": "http://cp.cloudflare.com/generate_204", "interval": "5m", "tolerance": 50},
    {"type": "anytls", "tag": "hk", "server": "hk.example", "server_port": 443, "password": "p", "tls": {"enabled": true, "server_name": "hk.example"}},
    {"type": "vless", "tag": "jp", "server": "jp.example", "server_port": 443, "uuid": "u", "flow": "xtls-rprx-vision",
      "tls": {"enabled": true, "server_name": "jp.example", "ech": {"enabled": true}},
      "transport": {"type": "ws", "path": "/ws", "headers": {"Host": "jp.example"}}},
    {"type": "shadowsocks", "tag": "ss", "server": "1.2.3.4", "server_port": 8388, "method": "2022-blake3-aes-128-gcm", "password": "k", "multiplex": {"enabled": true}},
    {"type": "ssh", "tag": "ssh", "server": "1.2.3.4", "server_port": 22},
    {"type": "direct", "tag": "direct"},
    {"type": "block", "tag": "block"},
    {"type": "dns", "tag": "dns-out"}
  ],
  "endpoints": [
    {"type": "wireguard", "tag": "wg", "address": ["10.0.0.2/32"], "private_key": "priv",
      "peers": [{"address": "5.6.7.8", "port": 51820, "public_key": "pub", "allowed_ips": ["10.0.0.0/24"]}]}
  ],
  "route": {
    "rules": [
      {"action": "sniff"},
      {"protocol": "dns", "outbound": "dns-out"},
      {"domain_suffix": ["a.example"], "rule_set": ["cn"], "outbound": "direct"},
      {"type": "logical", "mode": "and", "rules": [{"network": "udp"}, {"port": 443}], "action": "reject"},
      {"rule_set": ["ads"], "action": "reject", "method": "drop"},
      {"rule_set": ["remote-src"], "outbound": "Proxy"},
      {"rule_set": ["mislabeled", "geoip-us"], "outbound": "Auto"},
      {"inbound": ["tun-in"], "outbound": "block"},
      {"ip_cidr": ["10.0.0.0/24"], "outbound": "wg"}
    ],
    "rule_set": [
      {"type": "inline", "tag": "cn", "rules": [{"domain_suffix": ["cn"]}, {"ip_cidr": ["1.0.1.0/24"]}]},
      {"type": "inline", "tag": "ads", "rules": [{"domain_keyword": ["ads"]}]},
      {"type": "remote", "tag": "remote-src", "format": "source", "url": "https://rules.example/src.json"},
      {"type": "remote", "tag": "mislabeled", "format": "source", "url": "https://rules.example/mislabeled"},
      {"type": "remote", "tag": "geoip-us", "format": "binary", "url": "https://rules.example/geoip-us.srs"}
    ],
    "final": "Proxy",
    "auto_detect_interface": true
  },
  "experimental": {"cache_file": {"enabled": true}}
}`

func TestSingBoxSubscription(t *testing.T) {
	files := map[string]string{
		"https://rules.example/src.json":   `{"version": 3, "rules": [{"domain": ["x.example"]}, {"type": "logical", "mode": "or", "rules": [{"ip_cidr": ["9.9.9.0/24"]}]}]}`,
		"https://rules.example/mislabeled": "SRS\x01binary",
	}
	read := func(_, url string) ([]byte, error) {
		if s, ok := files[url]; ok {
			return []byte(s), nil
		}
		return nil, errors.New("not found")
	}
	cfg, err := subscription.Parse([]byte(singBoxSub), subscription.Options{File: "sub.json", ReadFile: read})
	if err != nil {
		t.Fatal(err)
	}
	nodes := map[string]ir.Node{}
	for _, n := range cfg.Nodes {
		nodes[n.Tag] = n
	}
	if len(cfg.Nodes) != 4 || nodes["ssh"].Tag != "" {
		t.Fatalf("节点：%+v", cfg.Nodes)
	}
	if n := nodes["hk"]; n.Protocol != "anytls" || n.Port != 443 || n.TLS == nil || n.TLS.ServerName != "hk.example" || n.Options["password"] != "p" {
		t.Errorf("anytls：%+v", n)
	}
	// IR 表达不了的 TLS 字段（ech）原样透传，能表达的传输层拆出来
	if n := nodes["jp"]; n.TLS != nil || n.Options["tls"] == nil || n.Transport == nil || n.Transport.Headers["Host"] != "jp.example" {
		t.Errorf("vless：%+v", n)
	}
	if n := nodes["ss"]; n.Options["multiplex"] == nil || n.Options["method"] != "2022-blake3-aes-128-gcm" {
		t.Errorf("ss：%+v", n)
	}
	if n := nodes["wg"]; n.Server != "5.6.7.8" || n.Port != 51820 || n.Options["public_key"] != "pub" {
		t.Errorf("wireguard：%+v", n)
	}
	groups := map[string]ir.Group{}
	for _, g := range cfg.Groups {
		groups[g.Tag] = g
	}
	if g := groups["Proxy"]; strings.Join(g.Members, ",") != "Auto,hk,DIRECT" || g.Default != "Auto" || g.Type != ir.GroupSelect {
		t.Errorf("Proxy：%+v", g)
	}
	if g := groups["Auto"]; g.Type != ir.GroupURLTest || g.Interval.Minutes() != 5 || g.Tolerance != 50 {
		t.Errorf("Auto：%+v", g)
	}

	// 规则：sniff / dns-out 由 Velum 处理；inbound 条件无对应，整条跳过
	if len(cfg.Rules) != 6 {
		for _, r := range cfg.Rules {
			t.Log(r.Source.Text)
		}
		t.Fatalf("规则数 %d", len(cfg.Rules))
	}
	if r := cfg.Rules[0]; r.Match.Logical != "or" || len(r.Match.Children) != 2 || r.Match.Children[1].Cond.RuleSet[0] != "rs:cn" ||
		r.Action.Outbound != ir.OutboundDirect {
		t.Errorf("规则集与域名混写应拆为 or：%+v", r)
	}
	if r := cfg.Rules[1]; r.Match.Logical != "and" || r.Action.Kind != ir.ActionReject {
		t.Errorf("逻辑规则：%+v", r)
	}
	if r := cfg.Rules[2]; r.Action.Kind != ir.ActionDrop {
		t.Errorf("reject drop：%+v", r)
	}
	if cfg.Final.Outbound != "Proxy" {
		t.Errorf("final：%+v", cfg.Final)
	}
	sets := map[string]ir.RuleSet{}
	for _, rs := range cfg.RuleSets {
		sets[rs.Tag] = rs
	}
	if rs := sets["rs:cn"]; rs.Kind != ir.RuleSetInline || len(rs.Rules) != 2 || !rs.NeedsResolve {
		t.Errorf("inline：%+v", rs)
	}
	// 含逻辑规则的 source 规则集交给内核加载
	if rs := sets["rs:remote-src"]; rs.Kind != ir.RuleSetRemote || rs.Format != "source" || !rs.NeedsResolve {
		t.Errorf("remote source：%+v", rs)
	}
	if rs := sets["rs:mislabeled"]; rs.Kind != ir.RuleSetRemote || rs.Format != "binary" {
		t.Errorf("声明为 source 的二进制内容：%+v", rs)
	}
	if rs := sets["rs:geoip-us"]; rs.Kind != ir.RuleSetRemote || rs.Format != "binary" || !rs.NeedsResolve {
		t.Errorf("binary：%+v", rs)
	}

	d := cfg.DNS
	if d.Final != "cf" || d.Strategy != "prefer_ipv4" || d.FakeIP == nil || d.FakeIP.Inet4Range != "198.18.0.0/15" {
		t.Errorf("dns：%+v", d)
	}
	if len(d.Rules) != 2 || d.Rules[1].Match.Cond.RuleSet[0] != "rs:cn" {
		t.Errorf("dns 规则（fakeip 规则由 Velum 追加，clash_mode 跳过）：%+v", d.Rules)
	}
	svs := map[string]ir.DNSServer{}
	for _, s := range d.Servers {
		svs[s.Tag] = s
	}
	if s := svs["cf"]; s.Type != "https" || s.Detour != "Proxy" || s.Resolver != "ali" || s.Path != "" {
		t.Errorf("doh：%+v", s)
	}
	if s := svs["old"]; s.Type != "tls" || s.Address != "8.8.8.8" {
		t.Errorf("旧写法：%+v", s)
	}
	if !cfg.IPv6 {
		t.Error("TUN 有 IPv6 地址")
	}
	var kinds []string
	for _, w := range cfg.Warnings {
		kinds = append(kinds, w.Category+"/"+w.Kind)
	}
	got := strings.Join(kinds, " ")
	for _, want := range []string{"node/ssh", "group/selector", "rule/inbound", "dns/clash_mode", "ruleset/format", "global/inbounds"} {
		if !strings.Contains(got, want) {
			t.Errorf("缺少告警 %s：%s", want, got)
		}
	}
}
