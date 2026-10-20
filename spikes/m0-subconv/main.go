// m0-subconv：M0 订阅转换损耗验证。
//
// Clash/Stash YAML → sing-box 配置 + 规则集源文件，然后：
//  1. 用 sing-box 自身（box.New）校验整份配置；
//  2. 用 Clash 语义参考实现生成正例 / 反例探测，交给 sing-box 规则匹配器比对，量化语义差异；
//  3. 输出私有明细报告（含名称）与可提交的匿名摘要。
package main

import (
	"bytes"
	"context"
	stdjson "encoding/json"
	"flag"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"

	box "github.com/sagernet/sing-box"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/srs"
	"github.com/sagernet/sing-box/include"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/route/rule"
	"github.com/sagernet/sing/common/json"
	M "github.com/sagernet/sing/common/metadata"
	"gopkg.in/yaml.v3"
)

var (
	cfgPath    = flag.String("config", os.ExpandEnv("$HOME/Library/Application Support/Stash/Core/config.yaml"), "Clash/Stash YAML")
	baseDir    = flag.String("base", "", "rule-provider 相对路径基准目录（默认为配置文件所在目录）")
	outDir     = flag.String("out", "/tmp/velum-subconv", "输出目录（含敏感信息，勿提交）")
	publicPath = flag.String("public", "", "匿名摘要输出路径（可提交）")
	label      = flag.String("label", "sample", "样本标签（写入匿名摘要）")
	mutateFlag = flag.String("mutate", "", "自检：故意引入语义错误（merge-process / suffix-subonly / drop-invert），验证探测框架有效")
)

func main() {
	flag.Parse()
	log.SetFlags(0)
	mutate = *mutateFlag
	if *baseDir == "" {
		// Stash 的 rule-provider 相对路径以 Core/resources 为基准
		*baseDir = filepath.Dir(*cfgPath)
		if st, err := os.Stat(filepath.Join(*baseDir, "resources")); err == nil && st.IsDir() {
			*baseDir = filepath.Join(*baseDir, "resources")
		}
	}
	_ = os.MkdirAll(*outDir, 0o700)

	raw, err := os.ReadFile(*cfgPath)
	if err != nil {
		log.Fatal(err)
	}
	var doc obj
	if err := yaml.Unmarshal(raw, &doc); err != nil {
		log.Fatalf("parse yaml: %v", err)
	}

	fs := &Findings{}
	t0 := time.Now()
	c, err := convert(doc, filepath.Dir(*cfgPath), *baseDir, *outDir, fs)
	if err != nil {
		log.Fatal(err)
	}
	convDur := time.Since(t0)
	cfgFile := filepath.Join(*outDir, "config.json")
	if err := writeJSON(cfgFile, c.Config); err != nil {
		log.Fatal(err)
	}

	ctx := box.Context(context.Background(), include.InboundRegistry(), include.OutboundRegistry(), include.EndpointRegistry(),
		include.DNSTransportRegistry(), include.ServiceRegistry(), include.CertificateProviderRegistry())

	// 1. sing-box 整体校验
	checkErr := checkConfig(ctx, cfgFile)

	// 2. 规则集：解析 + 编译 srs + 语义探测
	var rsStats []rsStat
	var ms0, ms1 runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&ms0)
	var keep []any
	for _, pv := range c.Providers {
		if pv.Missing {
			continue
		}
		st, rules := probeProvider(ctx, pv)
		keep = append(keep, rules)
		rsStats = append(rsStats, st)
	}
	inline := probeInline(ctx, c.Inline)
	runtime.GC()
	runtime.ReadMemStats(&ms1)
	heapRules := int64(ms1.HeapAlloc) - int64(ms0.HeapAlloc)
	runtime.KeepAlive(keep)

	rep := buildReport(fs, c, rsStats, inline, checkErr, convDur, heapRules)
	priv := filepath.Join(*outDir, "report-private.md")
	_ = os.WriteFile(priv, []byte(rep.private), 0o600)
	fmt.Println(rep.public)
	if *publicPath != "" {
		_ = os.WriteFile(*publicPath, []byte(rep.public), 0o644)
	}
	log.Printf("\n私有明细：%s\n生成配置：%s", priv, cfgFile)
}

func checkConfig(ctx context.Context, path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	opts, err := json.UnmarshalExtendedContext[option.Options](ctx, data)
	if err != nil {
		return fmt.Errorf("decode: %w", err)
	}
	instance, err := box.New(box.Options{Context: ctx, Options: opts})
	if err != nil {
		return fmt.Errorf("box.New: %w", err)
	}
	return instance.Close()
}

type mismatch struct {
	Type, Kind     string
	Expect, Actual bool
	From, Probe    string
}

type rsStat struct {
	pv         *provider
	entries    int
	probes     int
	mismatches []mismatch
	srsBytes   int
	jsonBytes  int
	loadErr    error
}

func probeProvider(ctx context.Context, pv *provider) (rsStat, []adapter.HeadlessRule) {
	st := rsStat{pv: pv, entries: len(pv.Entries)}
	data, err := os.ReadFile(pv.OutFile)
	if err != nil {
		st.loadErr = err
		return st, nil
	}
	st.jsonBytes = len(data)
	compat, err := json.UnmarshalExtendedContext[option.PlainRuleSetCompat](ctx, data)
	if err != nil {
		st.loadErr = err
		return st, nil
	}
	plain, err := compat.Upgrade()
	if err != nil {
		st.loadErr = err
		return st, nil
	}
	var buf bytes.Buffer
	if err := srs.Write(&buf, plain, compat.Version); err != nil {
		st.loadErr = fmt.Errorf("srs: %w", err)
	}
	st.srsBytes = buf.Len()
	var rules []adapter.HeadlessRule
	for _, r := range plain.Rules {
		hr, err := rule.NewHeadlessRule(ctx, r)
		if err != nil {
			st.loadErr = err
			return st, nil
		}
		rules = append(rules, hr)
	}
	ref := newClashMatcher(pv.Entries)
	for _, p := range genProbes(pv.Entries) {
		st.probes++
		expect := ref.match(p)
		actual := false
		for _, r := range rules {
			if r.Match(probeMetadata(p)) {
				actual = true
				break
			}
		}
		if expect != actual {
			st.mismatches = append(st.mismatches, mismatch{p.Type, p.Kind, expect, actual, p.From, probeDesc(p)})
		}
	}
	return st, rules
}

type inlineStat struct {
	rules, probes int
	byType        map[string]int
	mismatches    []mismatch
	errs          []string
}

// probeInline：内联规则（含 AND / OR / NOT）逐条转为 headless rule，用语法树参考求值比对。
func probeInline(ctx context.Context, rules []inlineRule) inlineStat {
	st := inlineStat{byType: map[string]int{}}
	for _, ir := range rules {
		st.rules++
		typ := "LOGICAL-" + strings.ToUpper(ir.Node.Mode)
		if ir.Node.Leaf != nil {
			typ = ir.Node.Leaf.Type
		} else if ir.Node.Invert {
			typ = "LOGICAL-NOT"
		}
		st.byType[typ]++
		data, _ := stdjson.Marshal(ir.Headless)
		opt, err := json.UnmarshalExtendedContext[option.HeadlessRule](ctx, data)
		if err != nil {
			st.errs = append(st.errs, fmt.Sprintf("%s: %v", ir.Node.Raw, err))
			continue
		}
		hr, err := rule.NewHeadlessRule(ctx, opt)
		if err != nil {
			st.errs = append(st.errs, fmt.Sprintf("%s: %v", ir.Node.Raw, err))
			continue
		}
		for _, p := range ir.Node.candidates() {
			st.probes++
			expect, actual := ir.Node.eval(p), hr.Match(probeMetadata(p))
			if expect != actual {
				st.mismatches = append(st.mismatches, mismatch{typ, p.Kind, expect, actual, ir.Node.Raw, probeDesc(p)})
			}
		}
	}
	return st
}

func probeDesc(p Probe) string {
	var parts []string
	if p.Domain != "" {
		parts = append(parts, p.Domain)
	}
	if p.IP.IsValid() {
		parts = append(parts, p.IP.String())
	}
	if p.Proc != "" {
		parts = append(parts, "proc:"+p.Proc)
	}
	if p.Port != 0 {
		parts = append(parts, fmt.Sprintf("port:%d", p.Port))
	}
	if p.Network != "" {
		parts = append(parts, "net:"+p.Network)
	}
	return strings.Join(parts, " ")
}

func probeMetadata(p Probe) *adapter.InboundContext {
	md := &adapter.InboundContext{Network: orDefault(p.Network, "tcp")}
	if p.Domain != "" {
		md.Domain = p.Domain
		md.Destination = M.ParseSocksaddrHostPort(p.Domain, 443)
	}
	if p.IP.IsValid() {
		md.Destination = M.SocksaddrFrom(p.IP, 443)
		if p.IP.Is4() {
			md.IPVersion = 4
		} else {
			md.IPVersion = 6
		}
	}
	if p.Port != 0 {
		md.Destination.Port = p.Port
	}
	if p.Proc != "" {
		md.ProcessInfo = &adapter.ConnectionOwner{ProcessPath: "/probe/" + p.Proc}
	}
	return md
}

// ---------- 报告 ----------

type report struct{ public, private string }

func buildReport(fs *Findings, c *Converted, rs []rsStat, inline inlineStat, checkErr error, convDur time.Duration, heapRules int64) report {
	var pub, prv strings.Builder
	w := func(s string, a ...any) {
		fmt.Fprintf(&pub, s, a...)
		fmt.Fprintf(&prv, s, a...)
	}
	w("# 订阅转换损耗报告：%s\n\n", *label)
	w("- sing-box: v1.14.3，转换耗时 %s\n", convDur.Round(time.Millisecond))
	if checkErr == nil {
		w("- **sing-box 整体校验（box.New）：✅ 通过**\n")
	} else {
		fmt.Fprintf(&pub, "- **sing-box 整体校验（box.New）：❌ 失败**（详见私有报告）\n")
		fmt.Fprintf(&prv, "- **sing-box 整体校验（box.New）：❌ 失败**：`%v`\n", checkErr)
	}
	w("\n## 1. 配置项转换结论\n\n| 类别 | 支持 | 降级 | 不支持 |\n| :--- | ---: | ---: | ---: |\n")
	cats := []string{"节点", "节点源", "策略组", "规则", "规则集", "DNS", "全局"}
	// 节点 / 策略组按「对象」去重：一个对象只要有一条降级就算降级
	for _, cat := range cats {
		s, d, u := statusCounts(fs, cat)
		w("| %s | %d | %d | %d |\n", cat, s, d, u)
	}
	w("\n### 降级 / 不支持原因\n\n| 类别 | 类型 | 结论 | 原因 | 次数 |\n| :--- | :--- | :--- | :--- | ---: |\n")
	type key struct{ cat, kind, st, reason string }
	agg := map[key]int{}
	var keys []key
	for _, f := range fs.list {
		if f.Status == Supported {
			continue
		}
		k := key{f.Category, f.Kind, string(f.Status), f.Reason}
		if agg[k] == 0 {
			keys = append(keys, k)
		}
		agg[k]++
	}
	sort.Slice(keys, func(i, j int) bool {
		if keys[i].cat != keys[j].cat {
			return keys[i].cat < keys[j].cat
		}
		return keys[i].kind < keys[j].kind
	})
	for _, k := range keys {
		w("| %s | %s | %s | %s | %d |\n", k.cat, k.kind, k.st, k.reason, agg[k])
	}

	// 规则集
	w("\n## 2. 规则集语义等价探测\n\n")
	w("对每条原始规则生成正例与相似反例，期望值由 Clash 语义参考实现给出，实际值由 sing-box 规则匹配器给出。\n\n")
	totalE, totalP, totalM, totalSRS, totalJSON := 0, 0, 0, 0, 0
	typeCount := map[string]int{}
	unsup := map[string]int{}
	degr := map[string]int{}
	byBehavior := map[string]int{}
	splitProc := 0
	for _, s := range rs {
		totalE += s.entries
		totalP += s.probes
		totalM += len(s.mismatches)
		totalSRS += s.srsBytes
		totalJSON += s.jsonBytes
		byBehavior[s.pv.Behavior]++
		for k, v := range s.pv.Result.Counts {
			typeCount[k] += v
		}
		for k, v := range s.pv.Result.Unsupported {
			unsup[k] += v
		}
		for k, v := range s.pv.Result.Degraded {
			degr[k] += v
		}
		if s.pv.Result.HasProcess && len(s.pv.Result.Rules) > 1 {
			splitProc++
		}
	}
	w("- 规则集 %d 个（%s），规则 %d 条\n", len(rs), fmtMap(byBehavior), totalE)
	w("- 探测 %d 次，**语义不一致 %d 次**\n", totalP, totalM)
	w("- 含进程规则、已拆分为独立 rule 的规则集：%d 个\n", splitProc)
	w("- 体积：source JSON %s，编译为 .srs %s；加载为 sing-box 规则对象后堆内存增量约 %s\n", human(totalJSON), human(totalSRS), human(int(heapRules)))
	w("\n| 规则类型 | 条数 | 降级 | 不支持 |\n| :--- | ---: | ---: | ---: |\n")
	for _, t := range sortedKeys(typeCount) {
		w("| %s | %d | %d | %d |\n", t, typeCount[t], degr[t], unsup[t])
	}
	// 不一致明细
	mmAgg := map[string]int{}
	for _, s := range rs {
		for _, m := range s.mismatches {
			mmAgg[fmt.Sprintf("%s / %s / 期望=%v 实际=%v", m.Type, m.Kind, m.Expect, m.Actual)]++
		}
	}
	if len(mmAgg) > 0 {
		w("\n### 不一致分类\n\n| 规则类型 / 探测 / 结果 | 次数 |\n| :--- | ---: |\n")
		for _, k := range sortedKeys(mmAgg) {
			w("| %s | %d |\n", k, mmAgg[k])
		}
	}
	for _, s := range rs {
		if s.loadErr != nil {
			fmt.Fprintf(&pub, "\n- ⚠️ 有规则集加载失败（详见私有报告）\n")
			break
		}
	}

	// 内联规则
	w("\n## 3. 内联规则（含 AND / OR / NOT）语义等价探测\n\n")
	w("- 内联规则 %d 条（%s），探测 %d 次，**语义不一致 %d 次**\n", inline.rules, fmtMap(inline.byType), inline.probes, len(inline.mismatches))
	if len(inline.errs) > 0 {
		fmt.Fprintf(&pub, "- ⚠️ %d 条内联规则无法被 sing-box 加载（详见私有报告）\n", len(inline.errs))
		for _, e := range inline.errs {
			fmt.Fprintf(&prv, "- ⚠️ 加载失败：%s\n", e)
		}
	}
	imm := map[string]int{}
	for _, m := range inline.mismatches {
		imm[fmt.Sprintf("%s / %s / 期望=%v 实际=%v", m.Type, m.Kind, m.Expect, m.Actual)]++
		fmt.Fprintf(&prv, "- 不一致：`%s` 探测 `%s`：期望 %v，实际 %v\n", m.From, m.Probe, m.Expect, m.Actual)
	}
	if len(imm) > 0 {
		fmt.Fprintf(&pub, "\n| 规则类型 / 探测 / 结果 | 次数 |\n| :--- | ---: |\n")
		for _, k := range sortedKeys(imm) {
			fmt.Fprintf(&pub, "| %s | %d |\n", k, imm[k])
		}
	}

	// 私有明细
	fmt.Fprintf(&prv, "\n## 私有明细\n\n### 规则集\n\n| 名称 | behavior | 条数 | 探测 | 不一致 | srs | 错误 |\n| :--- | :--- | ---: | ---: | ---: | ---: | :--- |\n")
	for _, s := range rs {
		errS := ""
		if s.loadErr != nil {
			errS = s.loadErr.Error()
		}
		fmt.Fprintf(&prv, "| %s | %s | %d | %d | %d | %s | %s |\n", s.pv.Name, s.pv.Behavior, s.entries, s.probes, len(s.mismatches), human(s.srsBytes), errS)
	}
	fmt.Fprintf(&prv, "\n### 不一致样本（每个规则集最多 10 条）\n\n")
	for _, s := range rs {
		for i, m := range s.mismatches {
			if i >= 10 {
				fmt.Fprintf(&prv, "- …（%s 共 %d 条）\n", s.pv.Name, len(s.mismatches))
				break
			}
			fmt.Fprintf(&prv, "- [%s] `%s` 探测 `%s`（%s）：期望 %v，实际 %v\n", s.pv.Name, m.From, m.Probe, m.Kind, m.Expect, m.Actual)
		}
	}
	fmt.Fprintf(&prv, "\n### 全部降级 / 不支持项\n\n")
	for _, f := range fs.list {
		if f.Status != Supported {
			fmt.Fprintf(&prv, "- [%s/%s] %s %s：%s\n", f.Category, f.Kind, f.Status, f.Detail, f.Reason)
		}
	}
	return report{pub.String(), prv.String()}
}

// statusCounts 对节点 / 策略组按对象去重（取最差结论），其余类别按条目计数。
func statusCounts(fs *Findings, cat string) (s, d, u int) {
	if cat == "节点" || cat == "节点源" || cat == "策略组" || cat == "规则集" {
		worst := map[string]Status{}
		rank := map[Status]int{Supported: 0, Degraded: 1, Unsupported: 2}
		var order []string
		for _, f := range fs.list {
			if f.Category != cat {
				continue
			}
			if _, ok := worst[f.Detail]; !ok {
				order = append(order, f.Detail)
				worst[f.Detail] = f.Status
			} else if rank[f.Status] > rank[worst[f.Detail]] {
				worst[f.Detail] = f.Status
			}
		}
		for _, k := range order {
			switch worst[k] {
			case Supported:
				s++
			case Degraded:
				d++
			default:
				u++
			}
		}
		return
	}
	for _, f := range fs.list {
		if f.Category != cat {
			continue
		}
		switch f.Status {
		case Supported:
			s++
		case Degraded:
			d++
		default:
			u++
		}
	}
	return
}

func fmtMap(m map[string]int) string {
	var parts []string
	for _, k := range sortedKeys(m) {
		parts = append(parts, fmt.Sprintf("%s×%d", k, m[k]))
	}
	return strings.Join(parts, "、")
}

func human(n int) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1fMB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fKB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%dB", n)
}

func writeJSON(path string, v any) error {
	data, err := stdjson.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}
