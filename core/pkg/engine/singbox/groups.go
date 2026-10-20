package singbox

import (
	"context"
	"errors"
	"fmt"
	C "github.com/sagernet/sing-box/constant"
	"sync"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/urltest"
	"github.com/sagernet/sing/service"
)

func (e *Engine) runtime() (adapter.OutboundManager, context.Context, error) {
	e.lmu.Lock()
	defer e.lmu.Unlock()
	if e.instance == nil {
		return nil, nil, errors.New("引擎未运行")
	}
	return e.instance.Outbound(), e.bctx, nil
}

// SelectMember 立即切换 selector 组的成员（不重启引擎）。
func (e *Engine) SelectMember(group, member string) error {
	om, _, err := e.runtime()
	if err != nil {
		return err
	}
	ob, ok := om.Outbound(group)
	if !ok {
		return fmt.Errorf("策略组 %q 不存在", group)
	}
	sel, ok := ob.(interface{ SelectOutbound(string) bool })
	if !ok {
		return fmt.Errorf("策略组 %q 不可手动选择（自动测速组）", group)
	}
	if !sel.SelectOutbound(member) {
		return fmt.Errorf("%q 不是策略组 %q 的成员", member, group)
	}
	return nil
}

// TestGroup 对组内每个成员测速（毫秒；失败为 -1），并写入测速历史。URL 使用 sing-box 默认测速地址。
// group 为空时测试全部节点（测速历史按节点全局保存，各组共享）。
func (e *Engine) TestGroup(ctx context.Context, group string) (map[string]int, error) {
	om, bctx, err := e.runtime()
	if err != nil {
		return nil, err
	}
	var targets []adapter.Outbound
	if group == "" {
		for _, ob := range om.Outbounds() {
			if _, isGroup := ob.(adapter.OutboundGroup); isGroup || ob.Type() == C.TypeBlock || ob.Type() == C.TypeDNS {
				continue
			}
			targets = append(targets, ob)
		}
	} else {
		ob, ok := om.Outbound(group)
		if !ok {
			return nil, fmt.Errorf("策略组 %q 不存在", group)
		}
		g, ok := ob.(adapter.OutboundGroup)
		if !ok {
			return nil, fmt.Errorf("%q 不是策略组", group)
		}
		if ut, ok := ob.(adapter.URLTestGroup); ok {
			// 组内正在周期测速时 URLTest 会直接返回空结果，此时退回逐个成员测速
			if res, err := ut.URLTest(ctx); err == nil && len(res) > 0 {
				out := map[string]int{}
				for k, v := range res {
					out[k] = int(v)
				}
				return out, nil
			}
		}
		for _, tag := range g.All() {
			member, ok := om.Outbound(tag)
			if !ok {
				continue
			}
			if _, isGroup := member.(adapter.OutboundGroup); isGroup || member.Type() == C.TypeBlock {
				continue // 嵌套组按其自身测速结果；拦截无需测速
			}
			targets = append(targets, member)
		}
	}
	history := service.PtrFromContext[urltest.HistoryStorage](bctx)
	out := map[string]int{}
	var mu sync.Mutex
	var wg sync.WaitGroup
	sem := make(chan struct{}, 16)
	for _, member := range targets {
		wg.Add(1)
		go func(member adapter.Outbound) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			tctx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			d, err := urltest.URLTest(tctx, "", member)
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				out[member.Tag()] = -1
				return
			}
			out[member.Tag()] = int(d)
			if history != nil {
				history.StoreURLTestHistory(member.Tag(), &adapter.URLTestHistory{Time: time.Now(), Delay: d})
			}
		}(member)
	}
	wg.Wait()
	return out, nil
}

// Delay 返回最近一次测速结果（毫秒；无记录为 0）。
func (e *Engine) Delay(tag string) int {
	_, bctx, err := e.runtime()
	if err != nil {
		return 0
	}
	if h := service.PtrFromContext[urltest.HistoryStorage](bctx).LoadURLTestHistory(tag); h != nil {
		return int(h.Delay)
	}
	return 0
}
