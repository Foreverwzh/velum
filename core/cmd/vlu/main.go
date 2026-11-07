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
	"github.com/Foreverwzh/velum/core/pkg/i18n"
	"net"
	"net/http"
	"net/netip"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Foreverwzh/velum/core/pkg/config"
	"github.com/Foreverwzh/velum/core/pkg/engine"
	"github.com/Foreverwzh/velum/core/pkg/ir"
	"github.com/Foreverwzh/velum/core/pkg/rpc"
	"github.com/Foreverwzh/velum/core/pkg/source"
	"github.com/Foreverwzh/velum/core/pkg/sysproxy"
)

func main() {
	i18n.SetLocale(i18n.Detect())
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
			fmt.Println(i18n.T("velumd 已停止"))
		} else if sp := localSysProxy(paths); sp != nil {
			// velumd 已不在（如被 kill）：本地执行系统代理自愈
			switch res, herr := sp.Heal(); {
			case herr != nil:
				err = i18n.Errorf("velumd 未运行；系统代理自愈失败：%w", herr)
			case res == sysproxy.HealRestored:
				fmt.Println(i18n.T("velumd 未运行；系统代理已按快照还原"))
				err = nil
			case res == sysproxy.HealDiscarded:
				fmt.Println(i18n.T("velumd 未运行；系统代理已被修改，丢弃快照"))
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
			i18n.Printf("已重新加载：%d 节点 / %d 策略组 / %d 规则，%d 条告警\n", s.Subscription.Nodes, s.Subscription.Groups, s.Subscription.Rules, s.Warnings)
		}
	case "update":
		var s *rpc.Status
		if s, err = c.Update(); err == nil {
			i18n.Printf("已更新订阅 %s：%d 节点 / %d 规则，%d 条告警；规则集在后台更新\n", s.Subscription.Source, s.Subscription.Nodes, s.Subscription.Rules, s.Warnings)
			if s.Subscription.FetchErr != "" {
				i18n.Printf("⚠ 远程拉取失败，正在使用缓存：%s\n", s.Subscription.FetchErr)
			}
		}
	case "upgrade":
		err = upgrade(c, args)
	case "why":
		err = why(c, args)
	case "conns", "connections":
		err = conns(c, args)
	case "warnings":
		err = warnings(c)
	case "select":
		if len(args) != 2 {
			err = i18n.Errorf("用法：vlu select <策略组> <成员>")
			break
		}
		if _, err = c.Select(args[0], args[1]); err == nil {
			i18n.Printf("%s → %s（已保存，重启后保持）\n", args[0], args[1])
		}
	case "sub":
		err = subCmd(c, args)
	case "profile":
		err = profileCmd(c, args)
	case "test":
		if len(args) > 1 {
			err = i18n.Errorf("用法：vlu test [策略组]（不指定时测试全部节点）")
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
					fmt.Printf("  %-6s %s\n", i18n.T("失败"), k)
				} else {
					fmt.Printf("  %4dms %s\n", v, k)
				}
			}
		}
	case "env":
		var s *rpc.Status
		if s, err = c.Status(); err == nil {
			fmt.Print(proxyEnv(s.Listen, runtime.GOOS))
		}
	case "debug-cpu":
		err = debugCPU(c, args)
	case "repair":
		var r *rpc.RepairResponse
		if r, err = c.Repair(); err == nil {
			for _, s := range r.Steps {
				mark := "✓"
				switch {
				case s.Warn:
					mark = "!"
				case !s.OK:
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

// proxyEnv 生成让命令行程序经本地端口上网的环境变量设置（Windows 为 PowerShell 语法）。
// 不同程序读取的变量大小写不一，两种都设置；监听全部地址时经回环地址连接。
func proxyEnv(listen, goos string) string {
	if a, err := netip.ParseAddrPort(listen); err == nil && a.Addr().IsUnspecified() {
		lo := "127.0.0.1"
		if a.Addr().Is6() {
			lo = "::1"
		}
		listen = net.JoinHostPort(lo, strconv.Itoa(int(a.Port())))
	}
	vars := [][2]string{{"http_proxy", "http://" + listen}, {"https_proxy", "http://" + listen}, {"all_proxy", "socks5://" + listen}, {"no_proxy", "localhost,127.0.0.1,::1"}}
	var b strings.Builder
	for _, v := range vars {
		for _, name := range []string{v[0], strings.ToUpper(v[0])} {
			if goos == "windows" {
				fmt.Fprintf(&b, "$env:%s=\"%s\"\n", name, v[1])
			} else {
				fmt.Fprintf(&b, "export %s=%s\n", name, v[1])
			}
		}
	}
	return b.String()
}

func usage() {
	fmt.Fprint(os.Stderr, i18n.T(`用法：vlu <命令>
  up                启动 velumd（后台）
  env               输出经 Velum 上网的代理环境变量，如 eval "$(vlu env)"
  down              停止 velumd
  status            查看状态
  sysproxy on|off|config  系统代理开关（重启后保持，不改 config.yaml；config = 回到 config.yaml 的设置）
  tun on|off|config       TUN 开关（需先 sudo velum-helper）；可与系统代理同时开启
                          Linux：sudo vlu tun on --try 试运行，新开 SSH 连接用 sudo vlu tun confirm 确认
  reload            重新加载配置（config.yaml / overlay.yaml；订阅与规则集按更新间隔使用缓存）
  upgrade           升级 Velum 本身（Linux 服务器；校验签名，失败时恢复旧版本）
  update            重新拉取订阅并加载；provider 与规则集随后在后台全部重新拉取
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
  sub [订阅地址]      查看 / 设置当前订阅的地址（先拉取并校验，通过后才写入 config.yaml）
  profile           订阅列表（含流量额度）；有多个订阅时切换：
      use <序号|名称>     切换到该订阅
      add <地址> [名称]   添加订阅（先拉取并校验，不切换）
      update <序号|名称>  重新拉取订阅（更新流量额度）
      rm <序号|名称>      删除订阅（正在使用的不能删除）
  test [组]         对策略组成员测速；不指定组时测试全部节点
  repair            一键修复：清空 DNS 缓存、重新检测网络环境、还原残留的系统 DNS、检查其他代理工具的残留、
                    重新拉取订阅并加载（规则集在后台更新）、校正系统代理、节点测速
`))
}

func fatal(err error) {
	msg := err.Error()
	if strings.Contains(msg, "permission denied") && strings.Contains(msg, "velumd.sock") {
		msg = i18n.T("没有权限访问 velumd：把当前用户加入 velum 组（sudo usermod -aG velum $USER）后重新登录")
	}
	if strings.Contains(msg, "connect: no such file") || strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "cannot find the file") { // Windows：Named Pipe 不存在
		msg = i18n.T("velumd 未运行（先执行 vlu up）")
	}
	fmt.Fprintln(os.Stderr, "vlu:", msg)
	os.Exit(1)
}

// ---------------- up ----------------

func up(paths config.Paths, c *rpc.Client) error {
	if s, err := c.Status(); err == nil {
		i18n.Printf("velumd 已在运行（pid %d）\n", s.PID)
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
			i18n.Printf("velumd 已启动（pid %d）\n", s.PID)
			return printStatus(s)
		case err == nil && !said:
			fmt.Println(i18n.T("正在加载配置…"))
			said = true
		}
		time.Sleep(150 * time.Millisecond)
	}
	return i18n.Errorf("velumd 未能在 60 秒内就绪，查看 %s", paths.Log)
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

func setSysProxy(c *rpc.Client, args []string) error {
	return setSwitch(c, "系统代理", args)
}

// setSwitch：on / off / config（回到配置文件的设置）。两个开关相互独立，可同时开启。
func setSwitch(c *rpc.Client, which string, args []string) error {
	if len(args) != 1 || (args[0] != "on" && args[0] != "off" && args[0] != "config") {
		return i18n.Errorf("用法：vlu %s on|off|config", map[string]string{"系统代理": "sysproxy", "TUN": "tun"}[which])
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
		i18n.Printf("%s已开启\n", i18n.T(which))
		if which == "TUN" && s.TUN != nil {
			i18n.Printf("  %s %s，%d 条路由\n", s.TUN.Name, s.TUN.Address, len(s.TUN.Routes))
		}
		if which == "系统代理" && s.SysProxy.Overridden {
			i18n.Printf("  ⚠ 但实际生效的是 %s：主网络接口 %s 由其他 VPN / 代理工具接管，其代理设置优先\n", s.SysProxy.Effective, s.SysProxy.PrimaryInterface)
		}
	case want && s.Network != nil && s.Network.Paused != "":
		i18n.Printf("%s已暂停：%s\n", i18n.T(which), s.Network.Paused)
	default:
		i18n.Printf("%s已关闭\n", i18n.T(which))
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
	state := i18n.T("运行中")
	switch {
	case s.Starting:
		state = i18n.T("正在启动（加载配置）")
	case !s.Running:
		state = i18n.T("引擎未运行")
	}
	i18n.Printf("状态      %s（velumd %s，pid %d，目录 %s）\n", state, s.Version, s.PID, s.Home)
	if s.Listen != "" {
		onoff := func(on, want bool) string {
			switch {
			case on:
				return i18n.T("开")
			case want:
				return i18n.T("已暂停")
			}
			return i18n.T("关")
		}
		extra := ""
		if s.Overridden {
			extra = i18n.T("（由界面 / vlu 设置，覆盖 config.yaml）")
		}
		if s.WebListen != "" {
			i18n.Printf("网页界面  http://%s\n", s.WebListen)
		} else if s.WebError != "" {
			i18n.Printf("网页界面  没有启动：%s\n", s.WebError)
		}
		i18n.Printf("入口      %s；系统代理 %s，TUN %s%s\n", s.Listen, onoff(s.SystemProxyOn, s.WantSystemProxy), onoff(s.TUNOn, s.WantTUN), extra)
	}
	if n := s.Network; n != nil {
		i18n.Printf("网络      主接口 %s", n.PrimaryInterface)
		if n.Captive != "" {
			i18n.Printf("，连通性 %s", map[string]string{"online": i18n.T("正常"), "captive": i18n.T("需要网页认证"), "offline": i18n.T("离线")}[n.Captive])
		}
		fmt.Println()
		for _, v := range n.VPNs {
			if v.Full {
				i18n.Printf("          外部 VPN %s：全局接管（其他 VPN 或代理工具的 TUN）\n", v.Iface)
			} else {
				i18n.Printf("          外部 VPN %s：分流 %d 个网段、%d 个域名（经该 VPN 直连）\n", v.Iface, len(v.Routes), len(v.Domains))
			}
		}
		if len(n.StaleDNS) > 0 {
			i18n.Printf("⚠ 系统 DNS  仍指向 %s（代理工具退出时没有还原）：请在网络设置中把 DNS 改回自动获取\n", strings.Join(n.StaleDNS, "、"))
		}
		if len(n.SplitDNSDomains) > 0 {
			i18n.Printf("          系统 DNS 分流：%s（交给系统解析器）\n", strings.Join(n.SplitDNSDomains, "、"))
		}
		if n.Paused != "" {
			i18n.Printf("⏸ 已暂停  %s\n", n.Paused)
		}
	}
	if u := s.Update; u != nil && u.Available {
		i18n.Printf("⬆ 新版本 %s（当前 %s）：%s\n", u.Latest, u.Current, i18n.T(upgradeHint))
	}
	if t := s.TUNTrial; t != nil {
		i18n.Printf("⏳ TUN 试运行，%s 后回退；新开 SSH 连接执行 sudo vlu tun confirm 确认\n", time.Until(t.Deadline).Round(time.Second))
	}
	if s.TUNReverted != "" {
		fmt.Printf("⚠ %s\n", s.TUNReverted)
	}
	if t := s.TUN; t != nil {
		routes := strings.Join(t.Routes, " ")
		if len(t.Routes) > 6 {
			routes = strings.Join(t.Routes[:6], " ") + i18n.T(" …（共 %d 条）", len(t.Routes))
		}
		i18n.Printf("TUN       %s %s %s；路由 %s\n", firstNonEmpty(t.Name, i18n.T("（未打开）")), t.Address, t.Address6, routes)
		if len(t.SystemDNS) > 0 {
			i18n.Printf("          系统 DNS 已指向 %s（退出时还原）\n", strings.Join(t.SystemDNS, ", "))
		}
	}
	if s.SysProxy.Active {
		if s.SysProxy.Overridden {
			eff := s.SysProxy.Effective
			if eff == "" {
				eff = i18n.T("无")
			}
			i18n.Printf("系统代理  ⚠ 已写入各网络服务，但实际生效的是 %s：主网络接口 %s 由其他 VPN / 代理工具接管，其代理设置优先\n", eff, s.SysProxy.PrimaryInterface)
		} else {
			fmt.Println(i18n.T("系统代理  已由 Velum 接管并生效（退出时还原）"))
		}
	}
	if sub := s.Subscription; sub.Source != "" {
		name := ""
		if sub.Name != "" {
			name = sub.Name + " · "
		}
		i18n.Printf("订阅      %s%s（%s，%s）\n", name, sub.Source, sub.Origin, sub.FetchedAt.Format("01-02 15:04"))
		if u := usageText(sub.Info); u != "" {
			i18n.Printf("流量      %s\n", u)
		}
		if sub.FetchErr != "" {
			i18n.Printf("          ⚠ 远程拉取失败，正在使用缓存：%s\n", sub.FetchErr)
		}
		i18n.Printf("内容      %d 节点 / %d 策略组 / %d 规则 / %d 规则集\n", sub.Nodes, sub.Groups, sub.Rules, sub.RuleSets)
	} else if s.Listen != "" {
		fmt.Println(i18n.T("订阅      未配置（编辑 config.yaml 的 subscription 后执行 vlu reload）"))
	}
	if s.Warnings > 0 {
		i18n.Printf("告警      %d 条（vlu warnings 查看）\n", s.Warnings)
	}
	if s.LastError != "" {
		i18n.Printf("错误      %s\n", s.LastError)
	}
	for _, g := range s.Groups {
		if g.Selected != "" {
			i18n.Printf("策略组    %s → %s\n", g.Tag, g.Selected)
		}
	}
	return nil
}

// ---------------- why ----------------

func why(c *rpc.Client, args []string) error {
	fs := flag.NewFlagSet("why", flag.ExitOnError)
	live := fs.Bool("live", false, i18n.T("对照最近的实际连接"))
	resolve := fs.String("resolve", "", i18n.T("解析结果（逗号分隔）"))
	process := fs.String("process", "", i18n.T("发起进程路径"))
	udp := fs.Bool("udp", false, i18n.T("按 UDP 推演"))
	asJSON := fs.Bool("json", false, i18n.T("输出 JSON"))
	all := fs.Bool("all", false, i18n.T("列出全部未命中的规则"))
	noResolve := fs.Bool("no-resolve", false, i18n.T("不向引擎查询解析结果"))
	target, rest := splitTarget(args)
	_ = fs.Parse(rest)
	if target == "" {
		return i18n.Errorf("用法：vlu why <目标> [--live]")
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
			return i18n.T("%s（目标改写为 %s）", outbound, override)
		}
		return outbound
	case ir.ActionReject:
		return i18n.T("拦截（REJECT）")
	case ir.ActionDrop:
		return i18n.T("丢弃（REJECT-DROP）")
	}
	return action
}

func printWhy(resp *rpc.WhyResponse, all bool) {
	r := resp.Report
	t := r.Target
	i18n.Printf("目标      %s → %s:%d\n\n", t.Raw, t.Host(), t.Port)

	fmt.Print(i18n.T("① 系统代理  "))
	switch {
	case r.SysProxy.Bypassed:
		i18n.Printf("命中豁免 %q（%s）\n", r.SysProxy.Pattern, src(*r.SysProxy.Source))
	case !r.SysProxy.Enabled:
		fmt.Println(i18n.T("未接管系统代理（经 Velum 代理端口访问时如下）"))
	default:
		fmt.Println(i18n.T("未命中豁免 → 请求进入 Velum"))
	}

	fmt.Print("② DNS       ")
	switch {
	case !r.DNS.Applicable:
		fmt.Println(i18n.T("目标是 IP，无需解析"))
	case len(r.DNS.Answer) > 0:
		i18n.Printf("hosts 覆盖 %s（%s）\n", strings.Join(r.DNS.Answer, ", "), src(*r.DNS.Source))
	case r.DNS.Source != nil:
		fmt.Printf("%s（%s）\n", r.DNS.Server, src(*r.DNS.Source))
	default:
		i18n.Printf("%s（默认）\n", r.DNS.Server)
	}
	switch {
	case len(resp.Resolved) > 0:
		by := map[string]string{"user": i18n.T("--resolve 指定"), "engine": i18n.T("引擎按 DNS 规则解析")}[resp.ResolvedBy]
		i18n.Printf("            解析结果 %s（%s）\n", strings.Join(resp.Resolved, ", "), by)
	case resp.ResolveErr != "":
		i18n.Printf("            解析失败：%s\n", resp.ResolveErr)
	}
	for tag, e := range resp.RuleSetErrors {
		i18n.Printf("            ⚠ 规则集 %s 无法用于推演：%s\n", tag, e)
	}

	fmt.Println(i18n.T("③ 规则"))
	misses := 0
	for _, tr := range r.Rules {
		switch {
		case tr.Action == ir.ActionResolve:
			fmt.Printf("   #%-3d ↻ %s\n", tr.Index, tr.Reason)
		case tr.Matched:
			i18n.Printf("   #%-3d ✓ %s\n          命中：%s\n", tr.Index, src(tr.Source), tr.Reason)
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
		i18n.Printf("   （另有 %d 条规则未命中，--all 查看原因）\n", misses)
	}
	d := r.Decision
	if d.RuleIndex < 0 {
		fmt.Printf("   final ✓ %s\n", src(d.Source))
	}

	fmt.Print(i18n.T("④ 出站      "))
	if len(resp.Chain) > 0 {
		fmt.Println(strings.Join(resp.Chain, " → "))
	} else {
		fmt.Println(actionText(d.Action, d.Outbound, d.OverrideAddress))
	}
	if d.OverrideAddress != "" {
		i18n.Printf("            目标地址改写为 %s（hosts 覆盖）\n", d.OverrideAddress)
	}
	for _, n := range r.Notes {
		i18n.Printf("\n提示：%s", n)
	}
	if len(r.Notes) > 0 {
		fmt.Println()
	}
}

func printLive(resp *rpc.WhyResponse, cs []engine.Connection) {
	fmt.Println(i18n.T("\n── 实际连接（最近 5 条）──"))
	if len(cs) == 0 {
		fmt.Println(i18n.T("没有记录。经 Velum 访问一次该目标后再执行（例如 curl -x http://<listen> <目标>）"))
		return
	}
	static := resp.Report.Decision.RuleIndex
	for _, c := range cs {
		state := i18n.T("进行中")
		if c.End != nil {
			state = fmt.Sprintf("%dms", c.DurationMS)
		}
		fmt.Printf("%s  %s  %s\n", c.Start.Format("15:04:05"), firstNonEmpty(c.Domain, c.Destination), state)
		idx := "final"
		if c.RuleIndex >= 0 {
			idx = fmt.Sprintf("#%d", c.RuleIndex)
		}
		i18n.Printf("   规则 %s %s\n", idx, src(c.Provenance))
		rejected := len(c.Chain) == 1 && (c.Chain[0] == ir.OutboundReject || strings.HasPrefix(c.Chain[0], "reject"))
		if rejected {
			fmt.Println(i18n.T("   出站 已按规则拦截"))
		} else {
			i18n.Printf("   出站 %s\n", strings.Join(c.Chain, " → "))
		}
		if len(c.Resolved) > 0 {
			i18n.Printf("   解析 %s\n", strings.Join(c.Resolved, ", "))
		}
		if c.Untracked {
			fmt.Println(i18n.T("   （被拦截，信息来自内核日志）"))
		} else if !rejected {
			fb := i18n.T("无下行数据")
			if c.FirstByteMS >= 0 {
				fb = i18n.T("首包 %dms", c.FirstByteMS)
			}
			fmt.Printf("   ↑%s ↓%s  %s", human(c.Upload), human(c.Download), fb)
			if c.Sniffed != "" {
				i18n.Printf("  嗅探 %s", c.Sniffed)
			}
			if c.Process != "" {
				i18n.Printf("  进程 %s", filepath.Base(c.Process))
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
			fmt.Println(i18n.T("   ⚠ 与静态推演不一致。可能原因：目标以 IP 访问但嗅探出了域名；应用自带 DoH；推演缺少 --resolve 解析结果；远程规则集"))
		}
		if !rejected && c.End != nil && c.Download == 0 && c.FirstByteMS < 0 && !c.Untracked && len(c.Errors) == 0 && c.DurationMS > 3000 {
			fmt.Println(i18n.T("   ⚠ 连接持续数秒但没有任何下行数据：上游可能无响应（若上游经过其他 TUN，拨号失败会表现为这种情况）"))
		}
	}
}

// ---------------- conns ----------------

func conns(c *rpc.Client, args []string) error {
	fs := flag.NewFlagSet("conns", flag.ExitOnError)
	n := fs.Int("n", 20, i18n.T("历史条数（0 表示不显示历史）"))
	follow := fs.Bool("f", false, i18n.T("实时跟随"))
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
		fmt.Println(i18n.T("没有告警"))
		return nil
	}
	sort.SliceStable(w.Warnings, func(i, j int) bool { return w.Warnings[i].Category < w.Warnings[j].Category })
	for _, x := range w.Warnings {
		lvl := i18n.T("降级")
		if x.Level == ir.Unsupported {
			lvl = i18n.T("不支持")
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
		return i18n.Errorf("用法：vlu sub [订阅地址]")
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
			i18n.Printf("本地文件 %s\n", sc.File)
		default:
			fmt.Println(i18n.T("未设置订阅（vlu sub <订阅地址>）"))
		}
		return nil
	}
	fmt.Println(i18n.T("正在拉取并校验订阅…"))
	s, err := c.SetSubscription(rpc.SetSubscriptionRequest{URL: args[0]})
	if err != nil {
		return err
	}
	i18n.Printf("订阅已更新：%d 节点 / %d 策略组 / %d 规则\n", s.Subscription.Nodes, s.Subscription.Groups, s.Subscription.Rules)
	return nil
}

// usageText 把订阅的流量额度与到期时间写成一行；没有提供时为空。
func usageText(in *source.Info) string {
	if in == nil {
		return ""
	}
	var parts []string
	if in.HasUsage() {
		used := in.Upload + in.Download
		parts = append(parts, i18n.T("已用 %s / %s（%.0f%%），剩余 %s", bytesText(used), bytesText(in.Total),
			float64(used)*100/float64(in.Total), bytesText(max(in.Total-used, 0))))
	}
	if !in.Expire.IsZero() {
		days := int(time.Until(in.Expire).Hours() / 24)
		switch {
		case days < 0:
			parts = append(parts, i18n.T("已于 ")+in.Expire.Format("2006-01-02")+i18n.T(" 到期"))
		default:
			parts = append(parts, i18n.T("%s 到期（%d 天后）", in.Expire.Format("2006-01-02"), days))
		}
	}
	return strings.Join(parts, "；")
}

func bytesText(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	f, i := float64(n), 0
	for f >= unit && i < 5 {
		f /= unit
		i++
	}
	return fmt.Sprintf("%.2f %s", f, []string{"B", "KB", "MB", "GB", "TB", "PB"}[i])
}

func profileCmd(c *rpc.Client, args []string) error {
	ps, err := c.Profiles()
	if err != nil {
		return err
	}
	find := func(key string) (rpc.ProfileStatus, error) {
		if n, err := strconv.Atoi(key); err == nil && n >= 1 && n <= len(ps) {
			return ps[n-1], nil
		}
		for _, p := range ps {
			if p.Name == key || p.ID == key {
				return p, nil
			}
		}
		return rpc.ProfileStatus{}, i18n.Errorf("没有找到订阅 %q（vlu profile 查看列表）", key)
	}
	if len(args) == 0 {
		if len(ps) == 0 {
			fmt.Println(i18n.T("还没有订阅（vlu profile add <订阅地址>）"))
		}
		for i, p := range ps {
			mark := " "
			if p.Active {
				mark = "●"
			}
			fmt.Printf("%s %d. %s  %s\n", mark, i+1, p.Name, p.Source)
			if u := usageText(p.Info); u != "" {
				fmt.Printf("     %s\n", u)
			}
		}
		return nil
	}
	var s *rpc.Status
	switch args[0] {
	case "use", "add", "update", "rm":
		if len(args) < 2 {
			return i18n.Errorf("用法：vlu profile %s <%s>", args[0], map[bool]string{true: i18n.T("地址> [名称"), false: i18n.T("序号|名称")}[args[0] == "add"])
		}
	default:
		return i18n.Errorf("未知的子命令 %q（use / add / update / rm）", args[0])
	}
	if args[0] == "add" {
		fmt.Println(i18n.T("正在拉取并校验订阅…"))
		if s, err = c.AddProfile(rpc.AddProfileRequest{URL: args[1], Name: strings.Join(args[2:], " ")}); err == nil {
			i18n.Printf("已添加：%s（vlu profile use %d 切换）\n", s.Profiles[len(s.Profiles)-1].Name, len(s.Profiles))
		}
		return err
	}
	p, err := find(strings.Join(args[1:], " "))
	if err != nil {
		return err
	}
	switch args[0] {
	case "use":
		if s, err = c.ActivateProfile(p.ID); err == nil {
			i18n.Printf("已切换到 %s：%d 节点 / %d 策略组 / %d 规则\n", p.Name, s.Subscription.Nodes, s.Subscription.Groups, s.Subscription.Rules)
		}
	case "update":
		fmt.Println(i18n.T("正在拉取订阅…"))
		if s, err = c.UpdateProfile(p.ID); err == nil {
			for _, q := range s.Profiles {
				if q.ID == p.ID {
					i18n.Printf("已更新 %s %s\n", q.Name, usageText(q.Info))
				}
			}
		}
	case "rm":
		if _, err = c.DeleteProfile(p.ID); err == nil {
			i18n.Printf("已删除 %s\n", p.Name)
		}
	}
	return err
}

// debugCPU 采样 velumd 的 CPU，写到文件后用 go tool pprof 查看。用法：vlu debug-cpu [秒数] [文件]。
func debugCPU(c *rpc.Client, args []string) error {
	seconds, file := "30", "velumd-cpu.pprof"
	if len(args) > 0 {
		seconds = args[0]
	}
	if len(args) > 1 {
		file = args[1]
	}
	if _, err := strconv.Atoi(seconds); err != nil {
		return i18n.Errorf("用法：vlu debug-cpu [秒数] [文件]")
	}
	b, err := c.Raw(http.MethodGet, "/debug/pprof/profile?seconds="+seconds, nil)
	if err != nil {
		return err
	}
	if err := os.WriteFile(file, b, 0o600); err != nil {
		return err
	}
	i18n.Printf("已写入 %s\n", file)
	return nil
}
