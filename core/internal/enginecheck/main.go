// enginecheck：velum-engine 一致性测试的 Velum 侧。
//
// 用本机 Velum 的当前订阅与覆写编译 Plan（TUN 模式、iOS 平台，与 iOS 上实际生效的一致），
// 转成 velum-engine 的 IR 与规则集，并生成一批目标及 Velum 自己的路由判断（explain.WhyWith）。
// 引擎侧（velum-engine 仓库的 velum-tools route-batch）读同一批目标逐条比对。
//
//	go run ./internal/enginecheck -out DIR [-home VELUM_HOME] [-n 每个规则集取样数]
//
// 输出：DIR/ir.json、rule_sets.json、rule_index.json、warnings.json、targets.jsonl。含节点凭据，文件权限 0600。
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand/v2"
	"net/netip"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Foreverwzh/velum/core/pkg/compiler"
	"github.com/Foreverwzh/velum/core/pkg/config"
	"github.com/Foreverwzh/velum/core/pkg/engine/velum"
	"github.com/Foreverwzh/velum/core/pkg/explain"
	"github.com/Foreverwzh/velum/core/pkg/ir"
	"github.com/Foreverwzh/velum/core/pkg/overlay"
	"github.com/Foreverwzh/velum/core/pkg/source"
	"github.com/Foreverwzh/velum/core/pkg/subscription"
)

type target struct {
	Domain string `json:"domain,omitempty"`
	IP     string `json:"ip,omitempty"`
	// Resolved 是域名的解析结果（只有域名的目标可带）：覆盖「先解析、再按 IP 规则匹配」的路径。
	Resolved string `json:"resolved,omitempty"`
	Port     uint16 `json:"port"`
	Network  string `json:"network"`
	// Velum 的判断：命中的 Plan 规则下标（-1 = final）与归一化动作。
	RuleIndex int    `json:"rule_index"`
	Action    string `json:"action"`
	Uncertain bool   `json:"uncertain,omitempty"`
	// Velum 解析这个域名用的 DNS 服务器（引擎自己解析时的选择）；hosts 直接应答时为 "answer"。
	DNSServer string `json:"dns_server,omitempty"`
}

func main() {
	out := flag.String("out", "", "输出目录")
	home := flag.String("home", "", "Velum 目录（默认与 velumd 相同）")
	perSet := flag.Int("n", 40, "每个规则集取样的条目数")
	dumpDNS := flag.Bool("dump-dns", false, "只打印 Plan 的 DNS 部分（调试用）")
	flag.Parse()
	if *out == "" {
		fail(fmt.Errorf("缺少 -out"))
	}
	if *home == "" {
		h, err := config.Home()
		fail(err)
		*home = h
	}
	paths := config.PathsFor(*home)
	cfg, err := config.Load(paths)
	fail(err)
	f := source.New(paths.Cache, cfg.Subscription.UserAgent)
	h := sha256.Sum256([]byte(cfg.Subscription.URL))
	data, err := os.ReadFile(filepath.Join(paths.Cache, "subscription", hex.EncodeToString(h[:8])))
	fail(err)
	sub, err := subscription.Parse(data, subscription.Options{ReadFile: f.ProviderReader(), GeoRuleSetURL: cfg.Subscription.GeoRuleSetURL})
	fail(err)
	ov, err := overlay.Load(cfg.Overlay)
	fail(err)
	opts := compiler.Options{Mode: compiler.ModeTUN, Platform: "ios", DomainFirst: cfg.Rules.DomainFirst,
		TUN: compiler.TUNOptions{Address: netip.MustParsePrefix("198.18.0.1/30"), MTU: 1500, DNSHijack: true}}
	plan, err := compiler.Compile(sub, ov, opts)
	fail(err)

	geo := &velum.GeoSource{Fetch: func(url string) ([]byte, error) {
		b, _, err := f.Get("providers", url, 24*time.Hour)
		return b, err
	}}
	if *dumpDNS {
		d := plan.IR.DNS
		fmt.Printf("final=%s strategy=%q fakeip=%+v outbound_resolver=%q\n", d.Final, d.Strategy, d.FakeIP, d.OutboundResolver)
		for _, s := range d.Servers {
			fmt.Printf("server %-22s type=%-8s addr=%-40s members=%v fallback=%q resolver=%q detour=%q\n", s.Tag, s.Type, s.Address, s.Members, s.Fallback, s.Resolver, s.Detour)
		}
		for i, r := range d.Rules {
			b, _ := json.Marshal(r.Match)
			fmt.Printf("rule #%d match=%s qtype=%v server=%s answer=%v preferred_by=%q\n", i+1, b, r.QueryType, r.Server, r.Answer, r.PreferredBy)
		}
		return
	}
	r := velum.RenderWith(plan, velum.Options{Geo: geo})
	fail(os.MkdirAll(*out, 0o700))
	write := func(name string, v any) {
		b, err := json.MarshalIndent(v, "", " ")
		fail(err)
		fail(os.WriteFile(filepath.Join(*out, name), b, 0o600))
	}
	write("ir.json", r.Config)
	write("rule_sets.json", r.RuleSets)
	write("rule_index.json", r.RuleIndex)
	write("warnings.json", r.Warnings)

	ts := targets(plan.IR, *perSet, r.Loaded)
	tf, err := os.OpenFile(filepath.Join(*out, "targets.jsonl"), os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	fail(err)
	enc := json.NewEncoder(tf)
	for _, t := range ts {
		et := explain.Target{Domain: t.Domain, Port: t.Port, Network: t.Network}
		if t.IP != "" {
			et.IP = netip.MustParseAddr(t.IP)
		}
		if t.Resolved != "" {
			et.ResolvedIPs = []netip.Addr{netip.MustParseAddr(t.Resolved)}
		}
		rep := explain.WhyWith(plan, et, r.Loaded)
		d := rep.Decision
		t.RuleIndex, t.Action, t.Uncertain = d.RuleIndex, normalize(d.Action, d.Outbound), d.Uncertain
		if t.Domain != "" && rep.DNS.Applicable {
			t.DNSServer = rep.DNS.Server
			if len(rep.DNS.Answer) > 0 {
				t.DNSServer = "answer"
			}
		}
		fail(enc.Encode(t))
	}
	fail(tf.Close())

	kinds := map[string]int{}
	for _, w := range r.Warnings {
		kinds[w.Kind]++
	}
	fmt.Printf("规则 %d → %d 条，规则集 %d 个，告警 %v，目标 %d 个，写入 %s\n",
		len(plan.IR.Rules), len(r.Config.Rules), len(r.RuleSets), kinds, len(ts), *out)
}

// normalize 与引擎动作对齐：direct / reject / route:<目标>。
func normalize(kind, outbound string) string {
	switch kind {
	case ir.ActionRoute:
		switch outbound {
		case ir.OutboundDirect, "":
			return "direct"
		case ir.OutboundReject:
			return "reject"
		}
		return "route:" + outbound
	case ir.ActionReject, ir.ActionDrop:
		return "reject"
	}
	return kind
}

// targets 从规则和规则集里取样（命中样本），再加随机域名 / IP（大多落到 final）。
func targets(c *ir.Config, perSet int, loaded map[string][]ir.Cond) []target {
	rng := rand.New(rand.NewPCG(1, 2))
	var out []target
	seen := map[string]bool{}
	add := func(t target) {
		k := fmt.Sprintf("%s|%s|%s|%d|%s", t.Domain, t.IP, t.Resolved, t.Port, t.Network)
		if !seen[k] {
			seen[k] = true
			out = append(out, t)
		}
	}
	fromCond := func(cd ir.Cond, limit int) {
		n := 0
		take := func() bool { n++; return n <= limit }
		for _, d := range cd.Domain {
			if take() {
				add(target{Domain: d, Port: 443, Network: "tcp"})
			}
		}
		for _, s := range cd.DomainSuffix {
			if take() {
				base := strings.TrimPrefix(s, ".")
				add(target{Domain: "www." + base, Port: 443, Network: "tcp"})
				if !strings.HasPrefix(s, ".") {
					add(target{Domain: base, Port: 443, Network: "tcp"})
				}
			}
		}
		for _, k := range cd.DomainKeyword {
			if take() {
				add(target{Domain: "x" + k + "y.example", Port: 443, Network: "tcp"})
			}
		}
		for _, p := range cd.IPCIDR {
			if !take() {
				continue
			}
			pf, err := netip.ParsePrefix(p)
			if err != nil || !pf.Addr().Is4() {
				continue
			}
			add(target{IP: pf.Masked().Addr().String(), Port: 443, Network: "tcp"})
		}
	}
	for _, r := range c.Rules {
		if r.Match.Cond != nil {
			fromCond(*r.Match.Cond, 5)
		}
	}
	// DNS 规则里的域名也取样：覆盖每条 DNS 规则的服务器选择
	for _, r := range c.DNS.Rules {
		if r.Match.Cond != nil {
			fromCond(*r.Match.Cond, 5)
		}
	}
	for _, rs := range c.RuleSets {
		n := 0
		for _, cd := range rs.Rules {
			if n >= perSet {
				break
			}
			fromCond(cd, perSet-n)
			n += len(cd.Domain) + len(cd.DomainSuffix) + len(cd.DomainKeyword) + len(cd.IPCIDR)
		}
	}
	words := []string{"alpha", "beta", "cdn", "api", "img", "news", "shop", "mail", "static", "video"}
	tlds := []string{"com", "net", "org", "io", "cn", "jp", "dev"}
	for i := 0; i < 300; i++ {
		add(target{Domain: fmt.Sprintf("%s%d.%s.%s", words[rng.IntN(len(words))], rng.IntN(1000), words[rng.IntN(len(words))], tlds[rng.IntN(len(tlds))]), Port: 443, Network: "tcp"})
	}
	for i := 0; i < 300; i++ {
		a := netip.AddrFrom4([4]byte{byte(1 + rng.IntN(222)), byte(rng.IntN(256)), byte(rng.IntN(256)), byte(rng.IntN(256))})
		add(target{IP: a.String(), Port: 443, Network: "tcp"})
	}
	// 带解析结果的域名：一半配 geo 规则集里的地址（如中国 IP），一半配随机地址
	var geoIPs []string
	for _, conds := range loaded {
		for _, cd := range conds {
			for _, p := range cd.IPCIDR {
				if pf, err := netip.ParsePrefix(p); err == nil && pf.Addr().Is4() {
					geoIPs = append(geoIPs, pf.Masked().Addr().Next().String())
				}
			}
		}
	}
	sort.Strings(geoIPs)
	for i := 0; i < 400; i++ {
		d := fmt.Sprintf("r%d.%s.%s", i, words[rng.IntN(len(words))], tlds[rng.IntN(len(tlds))])
		ip := netip.AddrFrom4([4]byte{byte(1 + rng.IntN(222)), byte(rng.IntN(256)), byte(rng.IntN(256)), byte(rng.IntN(256))}).String()
		if i%2 == 0 && len(geoIPs) > 0 {
			ip = geoIPs[rng.IntN(len(geoIPs))]
		}
		add(target{Domain: d, Resolved: ip, Port: 443, Network: "tcp"})
	}
	// 嗅探：应用直接用 IP 连接，域名来自 TLS SNI / HTTP Host（域名 + 真实目标 IP，不再解析）
	n := len(out)
	for i := 0; i < n && i < 600; i++ {
		t := out[i]
		if t.Domain == "" || t.IP != "" {
			continue
		}
		ip := netip.AddrFrom4([4]byte{byte(1 + rng.IntN(222)), byte(rng.IntN(256)), byte(rng.IntN(256)), byte(rng.IntN(256))}).String()
		if i%2 == 0 && len(geoIPs) > 0 {
			ip = geoIPs[rng.IntN(len(geoIPs))]
		}
		add(target{Domain: t.Domain, IP: ip, Port: 443, Network: "tcp"})
	}
	// 端口 / 网络维度：同一批域名换成 UDP 443、TCP 80
	base := len(out)
	for i := 0; i < base && i < 200; i++ {
		t := out[i]
		t.Network, t.Port = "udp", 443
		add(t)
		t.Network, t.Port = "tcp", 80
		add(t)
	}
	sort.SliceStable(out, func(i, j int) bool { return false })
	return out
}

func fail(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
