package mobilebind

import (
	"encoding/json"
	"errors"
	"reflect"
	"sync"

	"github.com/Foreverwzh/velum/core/pkg/compiler"
	"github.com/Foreverwzh/velum/core/pkg/sysproxy"
)

// ProxyHost 是可选的宿主能力：把系统代理放进 VPN 的网络参数（macOS 系统扩展：NEProxySettings）。
// 系统随隧道撤销这些设置，扩展退出或崩溃后不会残留指向 Velum 的系统代理，因此不需要快照与自愈。
// 与 Host 分开：Android 的 Host 不必实现。
type ProxyHost interface {
	// SetProxy 设置系统代理；req 为 ProxyRequest 的 JSON，为空字符串表示清除。
	SetProxy(req string) error
}

// ProxyRequest 是交给宿主的系统代理设置：HTTP 与 HTTPS 代理都指向 Host:Port，Exceptions 不经代理。
type ProxyRequest struct {
	Host       string   `json:"host"`
	Port       int      `json:"port"`
	Exceptions []string `json:"exceptions,omitempty"`
}

// proxyHost 是 SetProxyHost 指定的宿主；为 nil 时不接管系统代理（Android）。
var proxyHost ProxyHost

// SetProxyHost 指定系统代理的宿主，在 Start 之前调用。
func SetProxyHost(h ProxyHost) {
	mu.Lock()
	defer mu.Unlock()
	proxyHost = h
}

// hostProxy 以 ProxyHost 实现 daemon.SysProxy。相同的设置不重复下发：宿主每次设置都要重新应用整个隧道参数。
type hostProxy struct {
	h   ProxyHost
	mu  sync.Mutex
	cur *ProxyRequest
}

func (p *hostProxy) Apply(plan compiler.SysProxyPlan) error {
	if plan.Host == "" || plan.Port == 0 {
		return errors.New("系统代理地址无效")
	}
	req := ProxyRequest{Host: plan.Host, Port: int(plan.Port)}
	for _, e := range plan.Exceptions {
		req.Exceptions = append(req.Exceptions, e.Pattern)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cur != nil && reflect.DeepEqual(*p.cur, req) {
		return nil
	}
	b, _ := json.Marshal(req)
	if err := p.h.SetProxy(string(b)); err != nil {
		return err
	}
	p.cur = &req
	return nil
}

func (p *hostProxy) Restore() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cur == nil {
		return nil
	}
	if err := p.h.SetProxy(""); err != nil {
		return err
	}
	p.cur = nil
	return nil
}

func (p *hostProxy) HasSnapshot() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.cur != nil
}

// Heal：系统随隧道撤销代理设置，没有可自愈的残留。
func (p *hostProxy) Heal() (sysproxy.HealResult, error) { return sysproxy.HealNothing, nil }

// ClearOwn：没有接管时隧道参数里不含代理，不会有指向 Velum 的残留。
func (p *hostProxy) ClearOwn(string, int) (bool, error) { return false, nil }
