// Package iostunnel 是 iOS 隧道扩展的 Go 入口，经 gomobile bind 导出。只运行内核，不含 daemon：
// 扩展内存上限约 50MB（spikes/ios-memory），订阅转换和编译都在 App 进程，配置由 App 写到共享目录。
package iostunnel

import (
	"context"
	"encoding/json"
	"github.com/Foreverwzh/velum/core/pkg/i18n"
	"io"
	"runtime/debug"
	"sync"
	"time"

	"github.com/Foreverwzh/velum/core/pkg/engine"
	"github.com/Foreverwzh/velum/core/pkg/engine/singbox"
	"github.com/Foreverwzh/velum/core/pkg/engine/tunnelproto"
	"github.com/Foreverwzh/velum/core/pkg/ir"
	"github.com/Foreverwzh/velum/core/pkg/logfile"
)

// historySize 是保留的已结束连接数。比桌面端小：每条记录都占扩展的内存。
const historySize = 200

// Host 由扩展实现。
type Host interface {
	// OpenTun 设置隧道网络参数，返回 utun 的 FD（所有权交给 Go）。req 与 mobilebind.TunRequest 相同。
	OpenTun(req string) (int32, error)
	// CloseTun 在核心关闭 TUN 后调用。
	CloseTun()
}

type opener struct{ host Host }

func (o opener) OpenTUN(r engine.TUNRequest) (int, string, io.Closer, error) {
	req, _ := json.Marshal(map[string]any{"mtu": r.MTU, "inet4": r.Inet4, "inet6": r.Inet6, "routes": r.Routes, "dns": r.SystemDNS})
	fd, err := o.host.OpenTun(string(req))
	if err != nil {
		return -1, "", nil, i18n.Errorf("建立隧道：%w", err)
	}
	if fd < 0 {
		return -1, "", nil, i18n.New("建立隧道：没有拿到 utun")
	}
	return int(fd), "utun", closer{o.host}, nil
}

type closer struct{ host Host }

func (c closer) Close() error { c.host.CloseTun(); return nil }

var (
	mu   sync.Mutex
	eng  *singbox.Engine
	logs io.Closer
	home string
	host Host
	tags []string
	// 当前配置的网络策略，以及最近一次报告的网络
	policies map[string]*ir.NetPolicy
	netKind  string
	netSSID  string
)

// Start 按共享目录里的配置启动内核。内存参数来自真机实测（高速下载峰值 30MB → 25MB）。
func Start(homeDir string, h Host) error {
	debug.SetGCPercent(50)
	debug.SetMemoryLimit(40 << 20)
	mu.Lock()
	defer mu.Unlock()
	if eng != nil {
		return i18n.New("内核已在运行")
	}
	home, host = homeDir, h
	return startLocked()
}

func startLocked() error {
	cfg, err := tunnelproto.Load(home, opener{host})
	if err != nil {
		return i18n.Errorf("读取配置：%w（先打开 Velum）", err)
	}
	lf, err := logfile.Open(tunnelproto.LogPath(home), 1<<20, 1)
	if err != nil {
		return err
	}
	e := singbox.New(singbox.Options{History: historySize, Log: lf})
	if err := e.Start(context.Background(), cfg); err != nil {
		_ = lf.Close()
		return err
	}
	eng, logs, tags = e, lf, outboundTags(cfg.JSON)
	for g, m := range tunnelproto.LoadSelected(home) {
		_ = e.SelectMember(g, m) // 成员可能已不在新订阅里：保持配置中的默认
	}
	policies = cfg.Policies
	applyPoliciesLocked()
	return nil
}

// applyPoliciesLocked 按当前网络切换策略组。手动切换不会被改回来，直到下一次换网或重新加载。
func applyPoliciesLocked() {
	if eng == nil {
		return
	}
	ir.ApplyNetPolicies(policies, netKind, netSSID, eng.SelectMember)
}

// NetworkChanged 由扩展在网络变化时调用：kind 为 wifi / cellular，其他情况传空字符串；ssid 是 Wi-Fi 名称（取不到时为空）。
func NetworkChanged(kind, ssid string) {
	mu.Lock()
	defer mu.Unlock()
	if kind == netKind && ssid == netSSID {
		return
	}
	netKind, netSSID = kind, ssid
	applyPoliciesLocked()
}

func stopLocked() {
	if eng != nil {
		_ = eng.Stop()
		eng = nil
	}
	if logs != nil {
		_ = logs.Close()
		logs = nil
	}
}

// SetLocale 设置语言。
func SetLocale(l string) { i18n.SetLocale(l) }

// Stop 停止内核。
func Stop() {
	mu.Lock()
	defer mu.Unlock()
	stopLocked()
}

// outboundTags 取配置中的全部出站，用于批量查询选择和延迟。
func outboundTags(js []byte) []string {
	var c struct {
		Outbounds []struct {
			Tag string `json:"tag"`
		} `json:"outbounds"`
	}
	if json.Unmarshal(js, &c) != nil {
		return nil
	}
	out := make([]string, 0, len(c.Outbounds))
	for _, o := range c.Outbounds {
		out = append(out, o.Tag)
	}
	return out
}

// Handle 处理 App 消息：输入、输出都是 tunnelproto 的 JSON。
func Handle(req []byte) []byte {
	var r tunnelproto.Request
	var resp tunnelproto.Response
	if err := json.Unmarshal(req, &r); err != nil {
		resp.Error = err.Error()
	} else if err := handle(r, &resp); err != nil {
		resp.Error = err.Error()
	}
	b, _ := json.Marshal(resp)
	return b
}

func handle(r tunnelproto.Request, resp *tunnelproto.Response) error {
	mu.Lock()
	defer mu.Unlock()
	if r.Op == tunnelproto.OpReload {
		stopLocked()
		return startLocked()
	}
	if eng == nil {
		return i18n.New("内核未运行")
	}
	switch r.Op {
	case tunnelproto.OpState:
		s := &tunnelproto.State{Selected: map[string]string{}, Delays: map[string]int{}, Traffic: eng.Traffic(), TUNName: eng.TUNName()}
		for _, t := range tags {
			if v := eng.Selected(t); v != "" {
				s.Selected[t] = v
			}
			if d := eng.Delay(t); d != 0 {
				s.Delays[t] = d
			}
		}
		resp.State = s
	case tunnelproto.OpConns:
		resp.Connections = eng.Connections(r.Limit)
	case tunnelproto.OpSelect:
		return eng.SelectMember(r.Group, r.Member)
	case tunnelproto.OpTest:
		e := eng
		mu.Unlock() // 测速最长 20 秒，期间状态查询和切换不应被挡住
		defer mu.Lock()
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		res, err := e.TestGroup(ctx, r.Group)
		resp.Delays = res
		if err != nil && len(res) == 0 {
			return err
		}
	case tunnelproto.OpResolve:
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		addrs, err := eng.Resolve(ctx, r.Domain)
		if err != nil {
			return err
		}
		for _, a := range addrs {
			resp.Addrs = append(resp.Addrs, a.String())
		}
	default:
		return i18n.Errorf("未知操作 %q", r.Op)
	}
	return nil
}
