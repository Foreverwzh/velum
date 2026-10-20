package main

import (
	"fmt"
	"net"
	"net/netip"
	"time"

	"github.com/sagernet/sing-box/common/process"
	"golang.org/x/sys/unix"
)

func main() {
	ln, _ := net.Listen("tcp", "127.0.0.1:17999")
	conn, _ := ln.Accept()
	time.Sleep(200 * time.Millisecond)
	buf, _ := unix.SysctlRaw("net.inet.tcp.pcblist_n")
	src := netip.MustParseAddrPort(conn.RemoteAddr().String())
	o, err := process.FindDarwinConnectionOwner("tcp", src, netip.AddrPort{})
	p := ""
	if o != nil {
		p = o.ProcessPath
	}
	fmt.Printf("外部 curl：pcblist_n=%dB peer=%s owner=%q err=%v\n", len(buf), src, p, err)
}
