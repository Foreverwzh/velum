package subscription_test

import (
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Foreverwzh/velum/core/pkg/explain"
	"github.com/Foreverwzh/velum/core/pkg/ir"
	"github.com/Foreverwzh/velum/core/pkg/subscription"
)

func testdata(p string) string { d, _ := filepath.Abs(filepath.Join("../../testdata", p)); return d }

func parseCoverage(t *testing.T) *ir.Config {
	t.Helper()
	data, err := os.ReadFile(testdata("coverage/config.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	cfg, err := subscription.Parse(data, subscription.Options{File: "config.yaml", BaseDirs: []string{testdata("coverage")}})
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func TestCoverageConversion(t *testing.T) {
	cfg := parseCoverage(t)
	tags := map[string]ir.Node{}
	for _, n := range cfg.Nodes {
		tags[n.Tag] = n
	}
	groups := map[string]ir.Group{}
	for _, g := range cfg.Groups {
		groups[g.Tag] = g
	}
	// 不支持的节点被剔除并告警
	for _, bad := range []string{"ssr", "snell-v3", "ss-restls"} {
		if _, ok := tags[bad]; ok {
			t.Errorf("%s 应被剔除", bad)
		}
	}
	// shadow-tls 前置 + detour
	if n := tags["ss-shadowtls"]; n.Detour != "ss-shadowtls/shadow-tls" || n.Server != "" {
		t.Errorf("shadow-tls 映射错误：%+v", n)
	}
	// relay 改写为 detour 链
	if n, ok := tags["Chain"]; !ok || n.Detour != "Chain/hop2" || tags["Chain/hop2"].Detour != "Auto" {
		t.Errorf("relay 映射错误：%+v", n)
	}
	// 动态成员：provider filter + group filter
	if got := strings.Join(groups["Auto"].Members, ","); got != "🇭🇰 香港 01,🇭🇰 HK 02 IPLC" {
		t.Errorf("Auto 成员：%s", got)
	}
	for _, m := range groups["NoUDP"].Members {
		if strings.HasPrefix(m, "ss-") || m == "http-proxy" {
			t.Errorf("exclude-type 未生效：%s", m)
		}
	}
	// hysteria2 单端口转区间
	if got := strings.Join(tags["hy2-hop"].Ports, ","); got != "20000:30000,40000:40000" {
		t.Errorf("hy2 ports: %s", got)
	}
	// 告警带行号
	for _, w := range cfg.Warnings {
		if w.Source.Line == 0 && w.Category != "global" {
			t.Errorf("告警缺少行号：%+v", w)
		}
	}
	// 规则行号与原文
	if len(cfg.Rules) == 0 || cfg.Rules[0].Source.Line == 0 || cfg.Rules[0].Source.Text != "DOMAIN,ad.example.com,REJECT" {
		t.Errorf("规则来源：%+v", cfg.Rules[0].Source)
	}
}

// ---------- Clash 语义参考实现 vs IR 求值器（验证订阅转换不改变语义）----------

func clashMatch(e subscription.Entry, m explain.Meta) bool {
	d := strings.ToLower(m.Domain)
	v := strings.ToLower(e.Value)
	switch e.Type {
	case "DOMAIN":
		return d != "" && d == v
	case "DOMAIN-SUFFIX":
		v = strings.TrimPrefix(v, ".")
		return d != "" && (d == v || strings.HasSuffix(d, "."+v))
	case "DOMAIN-SUBONLY":
		return d != "" && strings.HasSuffix(d, "."+v)
	case "DOMAIN-SINGLE":
		return d != "" && strings.HasSuffix(d, "."+v) && !strings.Contains(strings.TrimSuffix(d, "."+v), ".")
	case "DOMAIN-KEYWORD":
		return d != "" && strings.Contains(d, v)
	case "DOMAIN-WILDCARD":
		return d != "" && regexp.MustCompile(subscription.GlobToRegex(e.Value)).MatchString(d)
	case "IP-CIDR", "IP-CIDR6":
		p, err := netip.ParsePrefix(e.Value)
		if err != nil {
			return false
		}
		if m.IP.IsValid() {
			return p.Contains(m.IP)
		}
		for _, ip := range m.ResolvedIPs {
			if p.Contains(ip) {
				return true
			}
		}
	case "PROCESS-NAME":
		return m.ProcessPath != "" && filepath.Base(m.ProcessPath) == e.Value
	case "DST-PORT":
		return m.Port != 0 && strings.TrimSpace(e.Value) == itoa(int(m.Port))
	case "NETWORK":
		n := m.Network
		if n == "" {
			n = "tcp"
		}
		return strings.EqualFold(n, e.Value)
	}
	return false
}

func itoa(i int) string { return strconv.Itoa(i) }

func TestRuleProviderSemantics(t *testing.T) {
	dirs := []string{testdata("coverage")}
	if os.Getenv("VELUM_TEST_STASH") != "" {
		home, _ := os.UserHomeDir()
		dirs = append(dirs, filepath.Join(home, "Library/Application Support/Stash/Core/resources"))
	}
	probes, mismatches := 0, 0
	for _, dir := range dirs {
		_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err != nil || info.IsDir() || !(strings.HasSuffix(path, ".txt") || strings.HasSuffix(path, ".yaml")) || strings.Contains(path, "providers") || strings.HasSuffix(path, "config.yaml") {
				return nil
			}
			data, _ := os.ReadFile(path)
			behavior := "classical"
			if strings.Contains(path, "cidr") || strings.HasSuffix(path, "_ip.txt") {
				behavior = "ipcidr"
			} else if strings.Contains(path, "domain") {
				behavior = "domain"
			}
			entries := subscription.ParseProvider(data, behavior, "")
			res := subscription.ConvertEntries(entries)
			set := ir.RuleSet{Tag: "x", Kind: ir.RuleSetInline, Rules: res.Conds}
			sets := explain.Sets{"x": set}
			for i := range res.Conds {
				for _, p := range explain.ProbesForCond(&res.Conds[i]) {
					probes++
					want := false
					for _, e := range entries {
						if clashMatch(e, p) {
							want = true
							break
						}
					}
					got := explain.EvalMatch(ir.Leaf(ir.Cond{RuleSet: []string{"x"}}), p, sets).Matched
					if supportedOnly(entries) && want != got {
						mismatches++
						if mismatches <= 10 {
							t.Errorf("%s probe %+v: clash=%v ir=%v", filepath.Base(path), p, want, got)
						}
					}
				}
			}
			return nil
		})
	}
	t.Logf("probes=%d mismatches=%d", probes, mismatches)
	if probes < 50 {
		t.Fatalf("探测太少：%d", probes)
	}
}

// supportedOnly：含不支持条目（IP-ASN 等）的规则集，参考实现与 IR 天然不同，跳过比对。
func supportedOnly(es []subscription.Entry) bool {
	for _, e := range es {
		switch e.Type {
		case "IP-ASN", "USER-AGENT", "URL-REGEX":
			return false
		}
	}
	return true
}

// 回归：格式不规范的行不能导致 panic，应作为不支持的类型计数。
func TestMalformedClassicalLine(t *testing.T) {
	es := subscription.ParseProvider([]byte("DOMAIN-SUFFIX,ok.com\nweird-line-without-comma\n"), "classical", "text")
	res := subscription.ConvertEntries(es)
	if len(res.Conds) != 1 || res.Unsupported["WEIRD-LINE-WITHOUT-COMMA"] != 1 {
		t.Fatalf("%+v", res)
	}
}

// 远程 provider 并发读取：总耗时接近最慢的一个，而不是逐个累加。
func TestProvidersPrefetchConcurrently(t *testing.T) {
	var b strings.Builder
	b.WriteString("proxies:\n  - {name: a, type: socks5, server: 1.1.1.1, port: 1}\nrule-providers:\n")
	for i := 0; i < 10; i++ {
		fmt.Fprintf(&b, "  p%d: {type: http, behavior: domain, url: \"https://example.com/%d\", path: ./p%d.yaml}\n", i, i, i)
	}
	b.WriteString("rules:\n")
	for i := 0; i < 10; i++ {
		fmt.Fprintf(&b, "  - RULE-SET,p%d,DIRECT\n", i)
	}
	b.WriteString("  - MATCH,a\n")
	var calls atomic.Int32
	start := time.Now()
	cfg, err := subscription.Parse([]byte(b.String()), subscription.Options{ReadFile: func(path, url string) ([]byte, error) {
		calls.Add(1)
		time.Sleep(300 * time.Millisecond)
		return []byte("payload:\n  - '+." + strings.TrimPrefix(url, "https://example.com/") + ".example'\n"), nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if d := time.Since(start); d > 1500*time.Millisecond {
		t.Fatalf("10 个 provider 用了 %v，应并发读取", d)
	}
	if calls.Load() != 10 {
		t.Fatalf("ReadFile 调用 %d 次，应为 10（预读结果被复用）", calls.Load())
	}
	if len(cfg.RuleSets) != 10 {
		t.Fatalf("规则集 %d 个", len(cfg.RuleSets))
	}
}

// 多个 nameserver 与 mihomo 一样竞速：生成 race 服务器作为默认 DNS，不再告警「仅使用首个」。
func TestDNSNameserverRace(t *testing.T) {
	cfg, err := subscription.Parse([]byte(`proxies:
  - {name: a, type: socks5, server: 1.1.1.1, port: 1}
dns:
  nameserver: [tls://1.1.1.1, https://doh.pub/dns-query]
  nameserver-policy:
    "+.corp.example": [223.5.5.5, 119.29.29.29]
rules:
  - MATCH,a
`), subscription.Options{})
	if err != nil {
		t.Fatal(err)
	}
	byTag := map[string]ir.DNSServer{}
	for _, s := range cfg.DNS.Servers {
		byTag[s.Tag] = s
	}
	if cfg.DNS.Final != "ns" || byTag["ns"].Type != "race" || strings.Join(byTag["ns"].Members, ",") != "ns-0,ns-1" {
		t.Fatalf("final=%s ns=%+v", cfg.DNS.Final, byTag["ns"])
	}
	if len(cfg.DNS.Rules) == 0 || byTag[cfg.DNS.Rules[0].Server].Type != "race" {
		t.Fatalf("nameserver-policy 多值也应竞速：%+v", cfg.DNS.Rules)
	}
	for _, w := range cfg.Warnings {
		if w.Kind == "nameserver" {
			t.Fatalf("不应再告警：%+v", w)
		}
	}
}
