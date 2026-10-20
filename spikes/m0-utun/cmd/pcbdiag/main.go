// pcbdiag：诊断 sing-box darwin 进程查询在当前 macOS 版本上是否可用。
package main

import (
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
	"syscall"

	"github.com/sagernet/sing-box/common/process"
	"golang.org/x/sys/unix"
)

func main() {
	rel, _ := syscall.Sysctl("kern.osrelease")
	fmt.Println("kern.osrelease =", rel)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	go func() { c, _ := ln.Accept(); defer c.Close(); select {} }()
	c, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		panic(err)
	}
	src := netip.MustParseAddrPort(c.LocalAddr().String())
	dst := netip.MustParseAddrPort(c.RemoteAddr().String())
	owner, err := process.FindDarwinConnectionOwner("tcp", src, dst)
	fmt.Printf("sing-box FindDarwinConnectionOwner(%s -> %s): owner=%+v err=%v\n", src, dst, owner, err)

	buf, _ := unix.SysctlRaw("net.inet.tcp.pcblist_n")
	fmt.Println("pcblist_n bytes =", len(buf))
	// xinpgen: 首字段 xig_len(uint32)，xig_count(uint32)
	gl := binary.NativeEndian.Uint32(buf[0:4])
	cnt := binary.NativeEndian.Uint32(buf[4:8])
	fmt.Printf("xinpgen len=%d count=%d\n", gl, cnt)
	// pcblist_n 每条记录由多个 xn_len 前缀的子结构组成；统计一条完整记录的长度
	off := int(gl)
	var kinds []string
	start := off
	for off+8 <= len(buf) && len(kinds) < 12 {
		l := binary.NativeEndian.Uint32(buf[off : off+4])
		k := binary.NativeEndian.Uint32(buf[off+4 : off+8])
		kinds = append(kinds, fmt.Sprintf("kind=%#x len=%d", k, l))
		if l == 0 {
			break
		}
		off += int((l + 7) &^ 7)
		if k == 0x20 && len(kinds) > 1 { // XSO_INPCB 再次出现前停止
			break
		}
	}
	fmt.Println("first record sub-structs:", kinds, "span:", off-start)
}
