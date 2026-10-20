// Package overlay 定义用户私有的覆盖层（overlay.yaml），例如本地开发直连。
//
//	dev_bypass:
//	  domains:            # "x" 根域 + 子域；".x" 仅子域；"=x" 精确匹配
//	    - corp.example.com
//	  cidrs:
//	    - 10.12.0.0/16
//	  hosts:              # 域名覆盖（不修改 /etc/hosts）
//	    staging.corp.example.com: 10.12.34.56
//	  processes:          # 进程名（iOS 不支持；Android 为包名）
//	    - SomeVPNClient
//	system_proxy:
//	  bypass:             # 系统代理豁免（原样写入系统设置；Velum 不内置任何条目）
//	    - localhost
//	    - 192.168.0.0/16
package overlay

import (
	"fmt"
	"net/netip"
	"os"
	"strings"

	"github.com/Foreverwzh/velum/core/pkg/userfs"
	"gopkg.in/yaml.v3"
)

// Item 是带行号的一项配置，用于 Provenance。
type Item struct {
	Value string
	Line  int
}

type Host struct {
	Domain string
	IP     netip.Addr
	Line   int
}

type Overlay struct {
	File      string
	Domains   []Item
	CIDRs     []Item
	Hosts     []Host
	Processes []Item
	// SysProxyBypass 是用户配置的系统代理豁免写法（system_proxy.bypass），原样使用。
	SysProxyBypass []Item
}

// DomainKind 表示 overlay 域名写法的语义。
type DomainKind int

const (
	DomainSuffix  DomainKind = iota // x：根域 + 子域
	DomainSubOnly                   // .x：仅子域
	DomainExact                     // =x：精确
)

// ParseDomain 解析 overlay 域名写法。
func ParseDomain(v string) (string, DomainKind) {
	v = strings.ToLower(strings.TrimSpace(v))
	switch {
	case strings.HasPrefix(v, "="):
		return v[1:], DomainExact
	case strings.HasPrefix(v, "*."):
		return v[2:], DomainSubOnly
	case strings.HasPrefix(v, "."):
		return v[1:], DomainSubOnly
	}
	return v, DomainSuffix
}

func Load(path string) (*Overlay, error) {
	data, err := userfs.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return &Overlay{File: path}, nil
		}
		return nil, err
	}
	o, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	o.File = path
	return o, nil
}

func Parse(data []byte) (*Overlay, error) {
	o := &Overlay{}
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	if len(root.Content) == 0 {
		return o, nil
	}
	if sp := get(root.Content[0], "system_proxy"); sp != nil {
		if n := get(sp, "bypass"); n != nil {
			for _, it := range n.Content {
				if v := strings.TrimSpace(it.Value); v != "" {
					o.SysProxyBypass = append(o.SysProxyBypass, Item{Value: v, Line: it.Line})
				}
			}
		}
	}
	bypass := get(root.Content[0], "dev_bypass")
	if bypass == nil {
		return o, nil
	}
	items := func(key string) []Item {
		var out []Item
		if n := get(bypass, key); n != nil {
			for _, it := range n.Content {
				out = append(out, Item{Value: strings.TrimSpace(it.Value), Line: it.Line})
			}
		}
		return out
	}
	o.Domains = items("domains")
	o.Processes = items("processes")
	for _, it := range items("cidrs") {
		p, err := netip.ParsePrefix(it.Value)
		if err != nil {
			a, err2 := netip.ParseAddr(it.Value)
			if err2 != nil {
				return nil, fmt.Errorf("line %d: invalid cidr %q", it.Line, it.Value)
			}
			p = netip.PrefixFrom(a, a.BitLen())
		}
		o.CIDRs = append(o.CIDRs, Item{Value: p.Masked().String(), Line: it.Line})
	}
	if h := get(bypass, "hosts"); h != nil {
		for i := 0; i+1 < len(h.Content); i += 2 {
			k, v := h.Content[i], h.Content[i+1]
			ip, err := netip.ParseAddr(strings.TrimSpace(v.Value))
			if err != nil {
				return nil, fmt.Errorf("line %d: invalid host ip %q", v.Line, v.Value)
			}
			o.Hosts = append(o.Hosts, Host{Domain: strings.ToLower(strings.TrimSpace(k.Value)), IP: ip, Line: k.Line})
		}
	}
	return o, nil
}

func get(m *yaml.Node, key string) *yaml.Node {
	if m == nil || m.Kind != yaml.MappingNode {
		return nil
	}
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}
