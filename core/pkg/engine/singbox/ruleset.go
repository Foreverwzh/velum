package singbox

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"

	"github.com/Foreverwzh/velum/core/pkg/ir"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/srs"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common/json"
	"github.com/sagernet/sing/service"
)

// LoadRuleSetFile 把本地规则集文件（.srs 二进制或 source JSON）读为 IR 条件，供静态推演（vlu why）求值。
// 只支持 default 规则；遇到 logical 规则返回错误（调用方应把该规则集视为无法静态求值）。
func LoadRuleSetFile(path, format string) ([]ir.Cond, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var compat option.PlainRuleSetCompat
	if format == "source" {
		compat, err = json.UnmarshalExtended[option.PlainRuleSetCompat](data)
	} else {
		compat, err = srs.Read(bytes.NewReader(data), true) // recover=true：还原域名 / IP 列表
	}
	if err != nil {
		return nil, fmt.Errorf("读取规则集 %s：%w", path, err)
	}
	plain, err := compat.Upgrade()
	if err != nil {
		return nil, err
	}
	var out []ir.Cond
	for _, r := range plain.Rules {
		if r.Type != "" && r.Type != C.RuleTypeDefault {
			return nil, errors.New("规则集包含逻辑规则，暂不支持静态求值")
		}
		d := r.DefaultOptions
		c := ir.Cond{
			Domain: d.Domain, DomainSuffix: d.DomainSuffix, DomainKeyword: d.DomainKeyword, DomainRegex: d.DomainRegex,
			IPCIDR: d.IPCIDR, SourceIPCIDR: d.SourceIPCIDR, Network: d.Network, ProcessName: d.ProcessName, ProcessPath: d.ProcessPath,
			NoResolve: true, // 是否需要先解析由引用处（RuleSet.NeedsResolve）决定
		}
		for _, p := range d.Port {
			c.Port = append(c.Port, int(p))
		}
		out = append(out, c)
	}
	return out, nil
}

// EncodeRuleSet 把 IR 条件编码为 .srs（二进制规则集），用于测试与预编译。
func EncodeRuleSet(conds []ir.Cond) ([]byte, error) {
	var rules []option.HeadlessRule
	for _, c := range conds {
		d := option.DefaultHeadlessRule{Domain: c.Domain, DomainSuffix: c.DomainSuffix, DomainKeyword: c.DomainKeyword,
			DomainRegex: c.DomainRegex, IPCIDR: c.IPCIDR, SourceIPCIDR: c.SourceIPCIDR, Network: c.Network,
			ProcessName: c.ProcessName, ProcessPath: c.ProcessPath}
		for _, p := range c.Port {
			d.Port = append(d.Port, uint16(p))
		}
		rules = append(rules, option.HeadlessRule{Type: C.RuleTypeDefault, DefaultOptions: d})
	}
	var buf bytes.Buffer
	err := srs.Write(&buf, option.PlainRuleSet{Rules: rules}, C.RuleSetVersion3)
	return buf.Bytes(), err
}

// Resolve 用运行中内核的 DNS 路由器解析域名（与真实连接使用同一套 DNS 规则），供静态推演补全解析结果。
func (e *Engine) Resolve(ctx context.Context, domain string) ([]netip.Addr, error) {
	e.lmu.Lock()
	bctx := e.bctx
	running := e.instance != nil
	e.lmu.Unlock()
	if !running || bctx == nil {
		return nil, errors.New("引擎未运行")
	}
	router := service.FromContext[adapter.DNSRouter](bctx)
	if router == nil {
		return nil, errors.New("DNS 路由器不可用")
	}
	return router.Lookup(ctx, domain, adapter.DNSQueryOptions{})
}
