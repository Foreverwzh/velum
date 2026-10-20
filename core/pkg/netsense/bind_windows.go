package netsense

import (
	"encoding/binary"

	"golang.org/x/sys/windows"
)

// IP_UNICAST_IF / IPV6_UNICAST_IF：让套接字只经指定网卡发送。IPv4 的网卡序号须为网络字节序。
const unicastIF = 31

func bindToInterface(fd uintptr, network string, ifIndex int) error {
	if len(network) > 0 && network[len(network)-1] == '6' {
		return windows.SetsockoptInt(windows.Handle(fd), windows.IPPROTO_IPV6, unicastIF, ifIndex)
	}
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], uint32(ifIndex))
	return windows.SetsockoptInt(windows.Handle(fd), windows.IPPROTO_IP, unicastIF, int(binary.NativeEndian.Uint32(b[:])))
}
