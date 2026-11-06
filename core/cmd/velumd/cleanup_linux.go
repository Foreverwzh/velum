package main

import (
	"errors"
	"fmt"
	"github.com/Foreverwzh/velum/core/pkg/i18n"

	"github.com/sagernet/netlink"
	"github.com/sagernet/nftables"
	"golang.org/x/sys/unix"
)

// sing-tun 的默认值（v0.9.7，见 sing-tun 的 tun.go、redirect.go、redirect_route_linux.go）。
// 升级 sing-box 时要核对，core/scripts/netns-tun.sh 会回归「崩溃后没有残留」。
const (
	nftTable        = "sing-box"
	tunRuleFirst    = 9000
	tunRuleLast     = 9010
	tunRuleFallback = 32768
	tunRuleRedirect = 1
	tunTable        = 2022
	tunOutputMark   = 0x2024
)

// staleRule 判断 ip rule 是否是 sing-tun 留下的：
//   - 9000～9010：sing-tun 的专用范围，它自己退出时也按这个范围删除；
//   - 32768：带输出标记、查 2022 表的回退规则；
//   - 1：查随机路由表的 auto_redirect 规则，没有任何匹配条件。
func staleRule(r netlink.Rule) bool {
	switch {
	case r.Priority >= tunRuleFirst && r.Priority <= tunRuleLast:
		return true
	case r.Priority == tunRuleFallback:
		return r.Mark == tunOutputMark && r.Table == tunTable // 解析出的规则不会设置 MarkSet，只看 Mark
	case r.Priority == tunRuleRedirect:
		return r.Table > 255 && !r.Src.IsValid() && !r.Dst.IsValid() && r.Mark == 0 && r.IifName == "" && r.OifName == ""
	}
	return false
}

// cleanupNet 清除 auto_redirect 在进程崩溃后留下的 nftables 表和 ip rule（spikes/ml-linux/README.md F3）。
// 用 netlink，不依赖 nft、ip 命令。没有残留时什么也不做。
func cleanupNet() error {
	var errs []error
	nft, err := nftables.New()
	if err != nil {
		errs = append(errs, fmt.Errorf("nftables：%w", err))
	} else if tables, err := nft.ListTablesOfFamily(nftables.TableFamilyINet); err != nil {
		errs = append(errs, i18n.Errorf("列出 nftables 表：%w", err))
	} else {
		for _, t := range tables {
			if t.Name == nftTable || t.Name == guardTable {
				nft.DelTable(t)
				if err := nft.Flush(); err != nil {
					errs = append(errs, i18n.Errorf("删除 nftables 表 %s：%w", t.Name, err))
				}
			}
		}
	}
	rules, err := netlink.RuleList(netlink.FAMILY_ALL)
	if err != nil {
		return errors.Join(append(errs, i18n.Errorf("列出 ip rule：%w", err))...)
	}
	for i := range rules {
		r := rules[i]
		if !staleRule(r) {
			continue
		}
		// 随机路由表里的路由不会随进程消失：规则删掉后没有人会查，一并清除
		if r.Priority == tunRuleRedirect {
			if routes, err := netlink.RouteListFiltered(r.Family, &netlink.Route{Table: r.Table}, netlink.RT_FILTER_TABLE); err == nil {
				for j := range routes {
					_ = netlink.RouteDel(&routes[j])
				}
			}
		}
		del := netlink.NewRule()
		del.Family, del.Priority = r.Family, r.Priority
		if err := netlink.RuleDel(del); err != nil && !errors.Is(err, unix.ENOENT) {
			errs = append(errs, i18n.Errorf("删除 ip rule %d：%w", r.Priority, err))
		}
	}
	return errors.Join(errs...)
}
