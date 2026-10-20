//go:build darwin

package netsense

import "golang.org/x/sys/unix"

func bindToInterface(fd uintptr, network string, ifIndex int) error {
	if len(network) > 0 && network[len(network)-1] == '6' {
		return unix.SetsockoptInt(int(fd), unix.IPPROTO_IPV6, unix.IPV6_BOUND_IF, ifIndex)
	}
	return unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_BOUND_IF, ifIndex)
}
