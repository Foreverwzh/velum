// vlu：Velum 命令行。
//
//	vlu up            启动 velumd（后台）
//	vlu down          停止 velumd
//	vlu status        查看状态
//	vlu reload        重新加载配置
//	vlu why <目标>    推演一个请求会怎么走；--live 对照实际连接
//	vlu conns [-f]    最近的连接（-f 实时跟随）
//	vlu warnings      订阅转换与编译告警
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Foreverwzh/velum/core/pkg/config"
	"github.com/Foreverwzh/velum/core/pkg/engine"
	"github.com/Foreverwzh/velum/core/pkg/ir"
	"github.com/Foreverwzh/velum/core/pkg/rpc"
	"github.com/Foreverwzh/velum/core/pkg/sysproxy"
)

func main() {
	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}
	h, err := config.Home()
	if err != nil {
		fatal(err)
	}
	paths := config.PathsFor(h)
	c := rpc.NewClient(paths.ClientAddrs()...)
	cmd, args := os.Args[1], os.Args[2:]
	switch cmd {
	case "up":
		err = up(paths, c)
	case "down":
		err = c.Stop()
		if err == nil {
			// 等 velumd 真正退出（它会先还原系统代理 / TUN），避免紧接着的 vlu up 复用正在退出的进程
			for i := 0; i < 100; i++ {
				if _, e := c.Status(); e != nil {
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
			fmt.Println("velumd 已停止")
		} else if sp := localSysProxy(paths); sp != nil {
			// velumd 已不在（如被 kill）：本地执行系统代理自愈
			switch res, herr := sp.Heal(); {
			case herr != nil:
				err = fmt.Errorf("velumd 未运行；系统代理自愈失败：%w", herr)
			case res == sysproxy.HealRestored:
				fmt.Println("velumd 未运行；系统代理已按快照还原")
				err = nil
			case res == sysproxy.HealDiscarded:
				fmt.Println("velumd 未运行；系统代理已被修改，丢弃快照")
				err = nil
			}
		}
	case "sysproxy":
		err = setSysProxy(c, args)
	case "tun":
		err = setTUN(c, args)
	case "status":
		err = status(c)
	case "reload":
		var s *rpc.Status
		if s, err = c.Reload(); err == nil {
			fmt.Printf("已重新加载：%d 节点 / %d 策略组 / %d 规则，%d 条告警\n", s.Subscription.Nodes, s.Subscription.Groups, s.Subscription.Rules, s.Warnings)
		}
	case "update":
		var s *rpc.Status
		if s, err = c.Update(); err == nil {
			fmt.Printf("已更新：订阅 %s（%s）；%d 节点 / %d 规则，%d 条告警\n", s.Subscription.Source, s.Subscription.Origin, s.Subscription.Nodes, s.Subscription.Rules, s.Warnings)
		}
	case "why":
		err = why(c, args)
	case "conns", "connections":
		err = conns(c, args)
	case "warnings":
		err = warnings(c)
	case "select":
		if len(args) != 2 {
			err = fmt.Errorf("用法：vlu select <策略组> <成员>")
			break
		}
		if _, err = c.Select(args[0], args[1]); err == nil {
			fmt.Printf("%s → %s（已保存，重启后保持）\n", args[0], args[1])
		}
	case "sub":
		err = subCmd(c, args)
	case "test":
		if len(args) > 1 {
			err = fmt.Errorf("用法：vlu test [策略组]（不指定时测试全部节点）")
			break
		}
		var r *rpc.TestResponse
		if r, err = c.Test(strings.Join(args, "")); err == nil {
			tags := make([]string, 0, len(r.Delays))
			for k := range r.Delays {
				tags = append(tags, k)
			}
			sort.Slice(tags, func(i, j int) bool { return delayKey(r.Delays[tags[i]]) < delayKey(r.Delays[tags[j]]) })
			for _, k := range tags {
				if v := r.Delays[k]; v < 0 {
					fmt.Printf("  %-6s %s\n", "失败", k)
				} else {
					fmt.Printf("  %4dms %s\n", v, k)
				}
			}
		}
	case "repair":
		var r *rpc.RepairResponse
		if r, err = c.Repair(); err == nil {
			for _, s := range r.Steps {
				mark := "✓"
				if !s.OK {
					mark = "✗"
				}
				fmt.Printf("%s %s", mark, s.Name)
				if s.Detail != "" {
					fmt.Printf("：%s", s.Detail)
				}
				fmt.Println()
			}
		}
	case "help", "-h", "--help":
		usage()
	default:
		usage()
		os.Exit(2)
	}
	if err != nil {
		fatal(err)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `用法：vlu <命令>
  up                启动 velumd（后台）
  down              停止 velumd
  status            查看状态
  sysproxy on|off|config  系统代理开关（重启后保持，不改 config.yaml；config = 回到 config.yaml 的设置）
  tun on|off|config       TUN 开关（需先 sudo velum-helper）；可与系统代理同时开启
  reload            重新加载配置（config.yaml / overlay.yaml；订阅与规则集按更新间隔使用缓存）
  update            强制重新拉取订阅、provider 与规则集并加载
  why <目标>        推演请求会怎么走。目标可以是 URL、host:port、域名或 IP
      --live            对照最近的实际连接
      --resolve <ip,..> 提供解析结果，用于推演 IP 规则
      --process <路径>  指定发起进程
      --udp             按 UDP 推演
      --no-resolve      不向引擎查询解析结果
      --json            输出 JSON
  conns [-n 20] [-f] 最近的连接；-f 实时跟随
  warnings          订阅转换与编译告警
  select <组> <成员>  切换策略组（保存，重启后保持）
  sub [订阅地址]      查看 / 设置订阅地址（先拉取并校验，通过后才写入 config.yaml）
  test [组]         对策略组成员测速；不指定组时测试全部节点
  repair            一键修复（DNS 缓存、网络环境、订阅、系统代理、测速）
`)
}

func fatal(err error) {
	msg := err.Error()
	if strings.Contains(msg, "connect: no such file") || strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "cannot find the file") { // Windows：Named Pipe 不存在
		msg = "velumd 未运行（先执行 vlu up）"
	}
	fmt.Fprintln(os.Stderr, "vlu:", msg)
	os.Exit(1)
}

// ---------------- up ----------------

func up(paths config.Paths, c *rpc.Client) error {
	if s, err := c.Status(); err == nil {
		fmt.Printf("velumd 已在运行（pid %d）\n", s.PID)
		return printStatus(s)
	}
	bin, err := velumdPath()
	if err != nil {
		return err
	}
	if err := config.Ensure(paths); err != nil {
		return err
	}
	if ok, err := startService(bin); ok || err != nil { // Windows：已安装服务时启动服务（含 TUN 能力）
		if err != nil {
			return err
		}
		return waitReady(paths, c)
	}
	out, err := os.OpenFile(filepath.Join(filepath.Dir(paths.Log), "velumd.out"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	cmd := exec.Command(bin, "--home", paths.Home)
	cmd.Stdout, cmd.Stderr = out, out
	cmd.SysProcAttr = detached()
	if err := cmd.Start(); err != nil {
		return err
	}
	_ = cmd.Process.Release()
	return waitReady(paths, c)
}

func waitReady(paths config.Paths, c *rpc.Client) error {
	// 本地接口很快就绪；首次加载（拉取订阅、规则集，启动引擎）可能需要更久
	deadline := time.Now().Add(60 * time.Second)
	said := false
	for time.Now().Before(deadline) {
		s, err := c.Status()
		switch {
		case err == nil && !s.Starting:
			fmt.Printf("velumd 已启动（pid %d）\n", s.PID)
			return printStatus(s)
		case err == nil && !said:
			fmt.Println("正在加载配置…")
			said = true
		}
		time.Sleep(150 * time.Millisecond)
	}
	return fmt.Errorf("velumd 未能在 60 秒内就绪，查看 %s", paths.Log)
}

func velumdPath() (string, error) {
	if self, err := os.Executable(); err == nil {
		// 经 /usr/local/bin/vlu 链接调用时，velumd 在链接目标（App 包内）旁边
		if real, err := filepath.EvalSymlinks(self); err == nil {
			self = real
		}
		p := filepath.Join(filepath.Dir(self), "velumd"+exeSuffix)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return exec.LookPath("velumd")
}

func setSysProxy(c *rpc.Client, args []string) error { return setSwitch(c, "系统代理", args) }

func setTUN(c *rpc.Client, args []string) error { return setSwitch(c, "TUN", args) }

// setSwitch：on / off / config（回到配置文件的设置）。两个开关相互独立，可同时开启。
func setSwitch(c *rpc.Client, which string, args []string) error {
	if len(args) != 1 || (args[0] != "on" && args[0] != "off" && args[0] != "config") {
		return fmt.Errorf("用法：vlu %s on|off|config", map[string]string{"系统代理": "sysproxy", "TUN": "tun"}[which])
	}
	req := rpc.SwitchRequest{}
	if which == "TUN" {
		req.TUN = args[0]
	} else {
		req.SystemProxy = args[0]
	}
	s, err := c.Switch(req)
	if err != nil {
		return err
	}
	if s.LastError != "" {
		return fmt.Errorf("%s", s.LastError)
	}
	on, want := s.SystemProxyOn, s.WantSystemProxy
	if which == "TUN" {
		on, want = s.TUNOn, s.WantTUN
	}
	switch {
	case on:
		fmt.Printf("%s已开启\n", which)
		if which == "TUN" && s.TUN != nil {
			fmt.Printf("  %s %s，%d 条路由\n", s.TUN.Name, s.TUN.Address, len(s.TUN.Routes))
		}
		if which == "系统代理" && s.SysProxy.Overridden {
			fmt.Printf("  ⚠ 但实际生效的是 %s：主网络接口 %s 由其他 VPN / 代理工具接管，其代理设置优先\n", s.SysProxy.Effective, s.SysProxy.PrimaryInterface)
		}
	case want && s.Network != nil && s.Network.Paused != "":
		fmt.Printf("%s已暂停：%s\n", which, s.Network.Paused)
	default:
		fmt.Printf("%s已关闭\n", which)
	}
	return nil
}

// ---------------- status ----------------

func status(c *rpc.Client) error {
	s, err := c.Status()
	if err != nil {
		return err
	}
	return printStatus(s)
}

func printStatus(s *rpc.Status) error {
	state := "运行中"
	switch {
	case s.Starting:
		state = "正在启动（加载配置）"
	case !s.Running:
		state = "引擎未运行"
	}
	fmt.Printf("状态      %s（velumd %s，pid %d，目录 %s）\n", state, s.Version, s.PID, s.Home)
	if s.Listen != "" {
		onoff := func(on, want bool) string {
			switch {
			case on:
				return "开"
			case want:
				return "已暂停"
			}
			return "关"
		}
		extra := ""
		if s.Overridden {
			extra = "（由界面 / vlu 设置，覆盖 config.yaml）"
		}
		fmt.Printf("入口      %s；系统代理 %s，TUN %s%s\n", s.Listen, onoff(s.SystemProxyOn, s.WantSystemProxy), onoff(s.TUNOn, s.WantTUN), extra)
	}
	if n := s.Network; n != nil {
		fmt.Printf("网络      主接口 %s", n.PrimaryInterface)
		if n.Captive != "" {
			fmt.Printf("，连通性 %s", map[string]string{"online": "正常", "captive": "需要网页认证", "offline": "离线"}[n.Captive])
		}
		fmt.Println()
		for _, v := range n.VPNs {
			if v.Full {
				fmt.Printf("          外部 VPN %s：全局接管（占据主路由）\n", v.Iface)
			} else {
				fmt.Printf("          外部 VPN %s：分流 %d 个网段、%d 个域名（经该 VPN 直连）\n", v.Iface, len(v.Routes), len(v.Domains))
			}
		}
		if len(n.SplitDNSDomains) > 0 {
			fmt.Printf("          系统 DNS 分流：%s（交给系统解析器）\n", strings.Join(n.SplitDNSDomains, "、"))
		}
		if n.Paused != "" {
			fmt.Printf("⏸ 已暂停  %s\n", n.Paused)
		}
	}
	if t := s.TUN; t != nil {
		routes := strings.Join(t.Routes, " ")
		if len(t.Routes) > 6 {
			routes = strings.Join(t.Routes[:6], " ") + fmt.Sprintf(" …（共 %d 条）", len(t.Routes))
		}
		fmt.Printf("TUN       %s %s %s；路由 %s\n", firstNonEmpty(t.Name, "（未打开）"), t.Address, t.Address6, routes)
		if len(t.SystemDNS) > 0 {
			fmt.Printf("          系统 DNS 已指向 %s（退出时还原）\n", strings.Join(t.SystemDNS, ", "))
		}
	}
	if s.SysProxy.Active {
		if s.SysProxy.Overridden {
			eff := s.SysProxy.Effective
			if eff == "" {
				eff = "无"
			}
			fmt.Printf("系统代理  ⚠ 已写入各网络服务，但实际生效的是 %s：主网络接口 %s 由其他 VPN / 代理工具接管，其代理设置优先\n", eff, s.SysProxy.PrimaryInterface)
		} else {
			fmt.Println("系统代理  已由 Velum 接管并生效（退出时还原）")
		}
	}
	if sub := s.Subscription; sub.Source != "" {
		fmt.Printf("订阅      %s（%s，%s）\n", sub.Source, sub.Origin, sub.FetchedAt.Format("01-02 15:04"))
		if sub.FetchErr != "" {
			fmt.Printf("          ⚠ 远程拉取失败，正在使用缓存：%s\n", sub.FetchErr)
		}
		fmt.Printf("内容      %d 节点 / %d 策略组 / %d 规则 / %d 规则集\n", sub.Nodes, sub.Groups, sub.Rules, sub.RuleSets)
	} else if s.Listen != "" {
		fmt.Println("订阅      未配置（编辑 config.yaml 的 subscription 后执行 vlu reload）")
	}
	if s.Warnings > 0 {
		fmt.Printf("告警      %d 条（vlu warnings 查看）\n", s.Warnings)
	}
	if s.LastError != "" {
		fmt.Printf("错误      %s\n", s.LastError)
	}
	for _, g := range s.Groups {
		if g.Selected != "" {
			fmt.Printf("策略组    %s → %s\n", g.Tag, g.Selected)
		}
	}
	return nil
}

// ---------------- why ----------------

func why(c *rpc.Client, args []string) error {
	fs := flag.NewFlagSet("why", flag.ExitOnError)
	live := fs.Bool("live", false, "对照最近的实际连接")
	resolve := fs.String("resolve", "", "解析结果（逗号分隔）")
	process := fs.String("process", "", "发起进程路径")
	udp := fs.Bool("udp", false, "按 UDP 推演")
	asJSON := fs.Bool("json", false, "输出 JSON")
	all := fs.Bool("all", false, "列出全部未命中的规则")
	noResolve := fs.Bool("no-resolve", false, "不向引擎查询解析结果")
	target, rest := splitTarget(args)
	_ = fs.Parse(rest)
	if target == "" {
		return fmt.Errorf("用法：vlu why <目标> [--live]")
	}
	req := rpc.WhyRequest{Target: target, Process: *process, NoResolve: *noResolve}
	if *resolve != "" {
		req.Resolved = strings.Split(*resolve, ",")
	}
	if *udp {
		req.Network = "udp"
	}
	resp, err := c.Why(req)
	if err != nil {
		return err
	}
	var liveConns []engine.Connection
	if *live {
		if liveConns, err = c.Connections(5, resp.Report.Target.Host()); err != nil {
			return err
		}
	}
	if *asJSON {
		return json.NewEncoder(os.Stdout).Encode(map[string]any{"why": resp, "live": liveConns})
	}
	printWhy(resp, *all)
	if *live {
		printLive(resp, liveConns)
	}
	return nil
}

// splitTarget 允许目标写在选项前或后。
func splitTarget(args []string) (string, []string) {
	var rest []string
	target := ""
	for i := 0; i < len(args); i++ {
		a := args[i]
		if strings.HasPrefix(a, "-") {
			rest = append(rest, a)
			if (a == "--resolve" || a == "--process" || a == "-resolve" || a == "-process") && i+1 < len(args) {
				rest = append(rest, args[i+1])
				i++
			}
			continue
		}
		if target == "" {
			target = a
		}
	}
	return target, rest
}

func src(s ir.Source) string {
	loc := s.Kind
	if s.File != "" {
		loc = s.File
	}
	if s.Line > 0 {
		loc += fmt.Sprintf(":%d", s.Line)
	}
	if s.Text != "" {
		return loc + "  " + s.Text
	}
	return loc
}

func actionText(action, outbound, override string) string {
	switch action {
	case ir.ActionRoute:
		if override != "" {
			return fmt.Sprintf("%s（目标改写为 %s）", outbound, override)
		}
		return outbound
	case ir.ActionReject:
		return "拦截（REJECT）"
	case ir.ActionDrop:
		return "丢弃（REJECT-DROP）"
	}
	return action
}

func printWhy(resp *rpc.WhyResponse, all bool) {
	r := resp.Report
	t := r.Target
	fmt.Printf("目标      %s → %s:%d\n\n", t.Raw, t.Host(), t.Port)

	fmt.Print("① 系统代理  ")
	switch {
	case r.SysProxy.Bypassed:
		fmt.Printf("命中豁免 %q（%s）\n", r.SysProxy.Pattern, src(*r.SysProxy.Source))
	case !r.SysProxy.Enabled:
		fmt.Println("未接管系统代理（经 Velum 代理端口访问时如下）")
	default:
		fmt.Println("未命中豁免 → 请求进入 Velum")
	}

	fmt.Print("② DNS       ")
	switch {
	case !r.DNS.Applicable:
		fmt.Println("目标是 IP，无需解析")
	case len(r.DNS.Answer) > 0:
		fmt.Printf("hosts 覆盖 %s（%s）\n", strings.Join(r.DNS.Answer, ", "), src(*r.DNS.Source))
	case r.DNS.Source != nil:
		fmt.Printf("%s（%s）\n", r.DNS.Server, src(*r.DNS.Source))
	default:
		fmt.Printf("%s（默认）\n", r.DNS.Server)
	}
	switch {
	case len(resp.Resolved) > 0:
		by := map[string]string{"user": "--resolve 指定", "engine": "引擎按 DNS 规则解析"}[resp.ResolvedBy]
		fmt.Printf("            解析结果 %s（%s）\n", strings.Join(resp.Resolved, ", "), by)
	case resp.ResolveErr != "":
		fmt.Printf("            解析失败：%s\n", resp.ResolveErr)
	}
	for tag, e := range resp.RuleSetErrors {
		fmt.Printf("            ⚠ 规则集 %s 无法用于推演：%s\n", tag, e)
	}

	fmt.Println("③ 规则")
	misses := 0
	for _, tr := range r.Rules {
		switch {
		case tr.Action == ir.ActionResolve:
			fmt.Printf("   #%-3d ↻ %s\n", tr.Index, tr.Reason)
		case tr.Matched:
			fmt.Printf("   #%-3d ✓ %s\n          命中：%s\n", tr.Index, src(tr.Source), tr.Reason)
		case tr.Unknown:
			fmt.Printf("   #%-3d ? %s\n          %s\n", tr.Index, src(tr.Source), tr.Reason)
		default:
			misses++
			if all {
				fmt.Printf("   #%-3d ✗ %s — %s\n", tr.Index, src(tr.Source), tr.Reason)
			}
		}
	}
	if !all && misses > 0 {
		fmt.Printf("   （另有 %d 条规则未命中，--all 查看原因）\n", misses)
	}
	d := r.Decision
	if d.RuleIndex < 0 {
		fmt.Printf("   final ✓ %s\n", src(d.Source))
	}

	fmt.Print("④ 出站      ")
	if len(resp.Chain) > 0 {
		fmt.Println(strings.Join(resp.Chain, " → "))
	} else {
		fmt.Println(actionText(d.Action, d.Outbound, d.OverrideAddress))
	}
	if d.OverrideAddress != "" {
		fmt.Printf("            目标地址改写为 %s（hosts 覆盖）\n", d.OverrideAddress)
	}
	for _, n := range r.Notes {
		fmt.Printf("\n提示：%s", n)
	}
	if len(r.Notes) > 0 {
		fmt.Println()
	}
}

func printLive(resp *rpc.WhyResponse, cs []engine.Connection) {
	fmt.Println("\n── 实际连接（最近 5 条）──")
	if len(cs) == 0 {
		fmt.Println("没有记录。经 Velum 访问一次该目标后再执行（例如 curl -x http://<listen> <目标>）")
		return
	}
	static := resp.Report.Decision.RuleIndex
	for _, c := range cs {
		state := "进行中"
		if c.End != nil {
			state = fmt.Sprintf("%dms", c.DurationMS)
		}
		fmt.Printf("%s  %s  %s\n", c.Start.Format("15:04:05"), firstNonEmpty(c.Domain, c.Destination), state)
		idx := "final"
		if c.RuleIndex >= 0 {
			idx = fmt.Sprintf("#%d", c.RuleIndex)
		}
		fmt.Printf("   规则 %s %s\n", idx, src(c.Provenance))
		rejected := len(c.Chain) == 1 && (c.Chain[0] == ir.OutboundReject || strings.HasPrefix(c.Chain[0], "reject"))
		if rejected {
			fmt.Println("   出站 已按规则拦截")
		} else {
			fmt.Printf("   出站 %s\n", strings.Join(c.Chain, " → "))
		}
		if len(c.Resolved) > 0 {
			fmt.Printf("   解析 %s\n", strings.Join(c.Resolved, ", "))
		}
		if c.Untracked {
			fmt.Println("   （被拦截，信息来自内核日志）")
		} else if !rejected {
			fb := "无下行数据"
			if c.FirstByteMS >= 0 {
				fb = fmt.Sprintf("首包 %dms", c.FirstByteMS)
			}
			fmt.Printf("   ↑%s ↓%s  %s", human(c.Upload), human(c.Download), fb)
			if c.Sniffed != "" {
				fmt.Printf("  嗅探 %s", c.Sniffed)
			}
			if c.Process != "" {
				fmt.Printf("  进程 %s", filepath.Base(c.Process))
			}
			fmt.Println()
		}
		for _, e := range c.Errors {
			if rejected && strings.Contains(e, "outbound/block") {
				continue // 拦截出站的预期报错
			}
			fmt.Printf("   ⚠ %s\n", e)
		}
		if c.RuleIndex != static {
			fmt.Println("   ⚠ 与静态推演不一致。可能原因：目标以 IP 访问但嗅探出了域名；应用自带 DoH；推演缺少 --resolve 解析结果；远程规则集")
		}
		if !rejected && c.End != nil && c.Download == 0 && c.FirstByteMS < 0 && !c.Untracked && len(c.Errors) == 0 && c.DurationMS > 3000 {
			fmt.Println("   ⚠ 连接持续数秒但没有任何下行数据：上游可能无响应（若上游经过其他 TUN，拨号失败会表现为这种情况）")
		}
	}
}

// ---------------- conns ----------------

func conns(c *rpc.Client, args []string) error {
	fs := flag.NewFlagSet("conns", flag.ExitOnError)
	n := fs.Int("n", 20, "历史条数（0 表示不显示历史）")
	follow := fs.Bool("f", false, "实时跟随")
	_ = fs.Parse(args)
	if *n > 0 {
		list, err := c.Connections(*n, "")
		if err != nil {
			return err
		}
		for i := len(list) - 1; i >= 0; i-- {
			printConnLine(list[i])
		}
	}
	if !*follow {
		return nil
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	err := c.Stream(ctx, printConnLine)
	if ctx.Err() != nil {
		return nil
	}
	return err
}

func printConnLine(c engine.Connection) {
	idx := "final"
	if c.RuleIndex >= 0 {
		idx = fmt.Sprintf("#%d", c.RuleIndex)
	}
	extra := ""
	if len(c.Errors) > 0 {
		extra = "  ⚠ " + c.Errors[0]
	}
	fmt.Printf("%s %-4s %-40s %-6s %-28s ↓%-8s %s%s\n", c.Start.Format("15:04:05"), c.Network,
		trunc(firstNonEmpty(c.Domain, c.Destination), 40), idx, trunc(strings.Join(c.Chain, "→"), 28), human(c.Download), trunc(c.Provenance.Text, 40), extra)
}

// ---------------- warnings ----------------

func warnings(c *rpc.Client) error {
	w, err := c.Warnings()
	if err != nil {
		return err
	}
	if len(w.Warnings) == 0 {
		fmt.Println("没有告警")
		return nil
	}
	sort.SliceStable(w.Warnings, func(i, j int) bool { return w.Warnings[i].Category < w.Warnings[j].Category })
	for _, x := range w.Warnings {
		lvl := "降级"
		if x.Level == ir.Unsupported {
			lvl = "不支持"
		}
		subject := ""
		if x.Subject != "" {
			subject = "「" + x.Subject + "」"
		}
		fmt.Printf("[%s/%s] %s%s：%s  (%s)\n", x.Category, x.Kind, lvl, subject, x.Reason, src(x.Source))
	}
	return nil
}

// ---------------- helpers ----------------

func delayKey(v int) int {
	if v < 0 {
		return 1 << 30
	}
	return v
}

func human(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1fM", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.1fK", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%dB", n)
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func subCmd(c *rpc.Client, args []string) error {
	if len(args) > 1 {
		return fmt.Errorf("用法：vlu sub [订阅地址]")
	}
	if len(args) == 0 {
		sc, err := c.Subscription()
		if err != nil {
			return err
		}
		switch {
		case sc.URL != "":
			fmt.Println(sc.URL)
		case sc.File != "":
			fmt.Printf("本地文件 %s\n", sc.File)
		default:
			fmt.Println("未设置订阅（vlu sub <订阅地址>）")
		}
		return nil
	}
	fmt.Println("正在拉取并校验订阅…")
	s, err := c.SetSubscription(rpc.SetSubscriptionRequest{URL: args[0]})
	if err != nil {
		return err
	}
	fmt.Printf("订阅已更新：%d 节点 / %d 策略组 / %d 规则\n", s.Subscription.Nodes, s.Subscription.Groups, s.Subscription.Rules)
	return nil
}
