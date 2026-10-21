package singbox

/*
#include <libproc.h>
#include <sys/proc_info.h>
#include <netinet/in.h>
#include <stdlib.h>

// 列出当前用户可见进程的 TCP / UDP 本地端口：依次写入 (协议, 端口, pid)，返回条数。
static int list_sockets(int *out, int cap) {
	int n = proc_listallpids(NULL, 0);
	if (n <= 0) return 0;
	n += 64;
	pid_t *pids = calloc(n, sizeof(pid_t));
	n = proc_listallpids(pids, n * sizeof(pid_t));
	int count = 0;
	for (int i = 0; i < n && count < cap; i++) {
		int sz = proc_pidinfo(pids[i], PROC_PIDLISTFDS, 0, NULL, 0);
		if (sz <= 0) continue;
		struct proc_fdinfo *fds = malloc(sz);
		sz = proc_pidinfo(pids[i], PROC_PIDLISTFDS, 0, fds, sz);
		for (int j = 0; j < sz / (int)sizeof(struct proc_fdinfo) && count < cap; j++) {
			if (fds[j].proc_fdtype != PROX_FDTYPE_SOCKET) continue;
			struct socket_fdinfo si;
			if (proc_pidfdinfo(pids[i], fds[j].proc_fd, PROC_PIDFDSOCKETINFO, &si, sizeof(si)) < (int)sizeof(si)) continue;
			int proto, port;
			if (si.psi.soi_kind == SOCKINFO_TCP) {
				proto = IPPROTO_TCP;
				port = ntohs(si.psi.soi_proto.pri_tcp.tcpsi_ini.insi_lport);
			} else if (si.psi.soi_kind == SOCKINFO_IN && si.psi.soi_protocol == IPPROTO_UDP) {
				proto = IPPROTO_UDP;
				port = ntohs(si.psi.soi_proto.pri_in.insi_lport);
			} else {
				continue;
			}
			out[count * 3] = proto;
			out[count * 3 + 1] = port;
			out[count * 3 + 2] = pids[i];
			count++;
		}
		free(fds);
	}
	free(pids);
	return count;
}
*/
import "C"

import (
	"net/netip"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/common/process"
)

// 连接归属查询。sing-box 在 macOS 上读系统连接表（net.inet.tcp.pcblist_n），但 macOS 27 起经 codesign
// 签名的进程（如打包进 App 的 velumd）只能读到自己的连接；此时改为经 libproc 逐个进程列出 socket
// （与 lsof 相同），可查到当前用户的所有进程。

type portKey struct {
	proto int
	port  uint16
}

var sockets struct {
	sync.Mutex
	at   time.Time
	pids map[portKey]int
}

const (
	socketsTTL       = 500 * time.Millisecond
	socketsMinRescan = 50 * time.Millisecond // 查不到时重新列出的最短间隔，避免 root 进程等查不到的连接反复扫描
	socketsCap       = 65536
)

// nativeOwner 表示由 Velum 自己查询连接归属（见 platform.FindConnectionOwner）。
const nativeOwner = true

func findOwner(proto int, src netip.AddrPort, dst netip.AddrPort) (*adapter.ConnectionOwner, error) {
	network := "tcp"
	if proto == syscall.IPPROTO_UDP {
		network = "udp"
	}
	if o, err := process.FindDarwinConnectionOwner(network, src, dst); err == nil && o.ProcessPath != "" {
		return o, nil
	}
	pid := socketOwner(portKey{proto, src.Port()})
	if pid == 0 {
		return nil, process.ErrNotFound
	}
	path, err := pidPath(pid)
	if err != nil {
		return nil, err
	}
	return &adapter.ConnectionOwner{ProcessID: uint32(pid), UserId: -1, ProcessPath: path}, nil
}

func socketOwner(k portKey) int {
	sockets.Lock()
	defer sockets.Unlock()
	age := time.Since(sockets.at)
	if pid, ok := sockets.pids[k]; ok && age < socketsTTL {
		return pid
	}
	if age < socketsMinRescan {
		return 0
	}
	buf := make([]C.int, socketsCap*3)
	n := int(C.list_sockets(&buf[0], C.int(socketsCap)))
	pids := make(map[portKey]int, n)
	for i := 0; i < n; i++ {
		k := portKey{int(buf[i*3]), uint16(buf[i*3+1])}
		if _, ok := pids[k]; !ok {
			pids[k] = int(buf[i*3+2])
		}
	}
	sockets.pids, sockets.at = pids, time.Now()
	return pids[k]
}

func pidPath(pid int) (string, error) {
	buf := make([]C.char, C.PROC_PIDPATHINFO_MAXSIZE)
	if n := C.proc_pidpath(C.int(pid), unsafe.Pointer(&buf[0]), C.uint32_t(len(buf))); n <= 0 {
		return "", process.ErrNotFound
	}
	return C.GoString(&buf[0]), nil
}
