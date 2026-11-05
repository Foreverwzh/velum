package main

import (
	"fmt"

	"github.com/sagernet/nftables"
	"github.com/sagernet/nftables/binaryutil"
	"github.com/sagernet/nftables/expr"
	"golang.org/x/sys/unix"
)

// guardTable 防止接管时断开已建立的连接。
//
// auto_redirect 的 output 链是 nat 类型，只对连接跟踪认为是新连接的包生效。机器上没有防火墙时连接跟踪平时不工作，
// 接管那一刻才开始：此时已建立的连接如果由本机先发包（如 SSH 正在输出），会被当成新连接重定向给 sing-box，
// sing-box 不认识它，回 RST，连接就断了。
//
// 这里在 sing-box 的链之前，给「新连接但不是 SYN」的出站 TCP 打上 sing-box 自己出站流量的标记，它的链看到就放行。
// 真正的新连接以 SYN 开始，不受影响。接管前已建立的出站连接因此继续直连，不会被接管。
const guardTable = "velum-guard"

const (
	tunConnMark  = 0x2024 // sing-tun 的 DefaultAutoRedirectOutputMark
	ctStateNew   = 0x08   // NF_CT_STATE_BIT(IP_CT_NEW)
	tcpFlagsOff  = 13     // TCP 头里标志位的偏移
	tcpFlagsMask = 0x12   // SYN | ACK
	tcpFlagSYN   = 0x02
)

func guardExprs() []expr.Any {
	return []expr.Any{
		&expr.Meta{Key: expr.MetaKeyL4PROTO, Register: 1},
		&expr.Cmp{Op: expr.CmpOpEq, Register: 1, Data: []byte{unix.IPPROTO_TCP}},
		&expr.Payload{DestRegister: 1, Base: expr.PayloadBaseTransportHeader, Offset: tcpFlagsOff, Len: 1},
		&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 1, Mask: []byte{tcpFlagsMask}, Xor: []byte{0}},
		&expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: []byte{tcpFlagSYN}},
		&expr.Ct{Key: expr.CtKeySTATE, Register: 1},
		&expr.Bitwise{SourceRegister: 1, DestRegister: 1, Len: 4, Mask: binaryutil.NativeEndian.PutUint32(ctStateNew), Xor: []byte{0, 0, 0, 0}},
		&expr.Cmp{Op: expr.CmpOpNeq, Register: 1, Data: []byte{0, 0, 0, 0}},
		&expr.Immediate{Register: 1, Data: binaryutil.NativeEndian.PutUint32(tunConnMark)},
		&expr.Ct{Key: expr.CtKeyMARK, Register: 1, SourceRegister: true},
	}
}

// setConnGuard 在接管前加上保护（必须早于 sing-box 建立它的 nft 表），退出接管后移除。重复调用没有影响。
func setConnGuard(on bool) error {
	nft, err := nftables.New()
	if err != nil {
		return err
	}
	tables, err := nft.ListTablesOfFamily(nftables.TableFamilyINet)
	if err != nil {
		return err
	}
	var cur *nftables.Table
	for _, t := range tables {
		if t.Name == guardTable {
			cur = t
		}
	}
	switch {
	case on && cur != nil, !on && cur == nil:
		return nil
	case !on:
		nft.DelTable(cur)
		return nft.Flush()
	}
	t := nft.AddTable(&nftables.Table{Name: guardTable, Family: nftables.TableFamilyINet})
	policy := nftables.ChainPolicyAccept
	c := nft.AddChain(&nftables.Chain{Name: "output", Table: t, Type: nftables.ChainTypeFilter,
		Hooknum: nftables.ChainHookOutput, Priority: nftables.ChainPriorityMangle, Policy: &policy})
	nft.AddRule(&nftables.Rule{Table: t, Chain: c, Exprs: guardExprs()})
	if err := nft.Flush(); err != nil {
		return fmt.Errorf("加载连接保护规则：%w", err)
	}
	return nil
}
