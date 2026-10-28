package mobilebind

import "golang.org/x/sys/unix"

// tunName 返回 TUN 的网卡名。macOS 的 NetworkExtension 交来的是 utun 控制套接字，内核需要真实的 utunN 名字。
func tunName(fd int) string {
	if name, err := unix.GetsockoptString(fd, 2 /* SYSPROTO_CONTROL */, 2 /* UTUN_OPT_IFNAME */); err == nil && name != "" {
		return name
	}
	return "tun"
}
