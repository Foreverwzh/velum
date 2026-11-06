//go:build darwin && !ios

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
	"os"
	"sync"
	"sync/atomic"
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

// pcblistMisses 统计系统连接表连续漏掉别的进程的次数。读一次要拷贝整张表，读不到别的进程时就是白费 CPU。
var pcblistMisses atomic.Int32

var sockets struct {
	sync.Mutex
	at   time.Time // 最近一次列出的开始时间
	pids map[portKey]int
}

const (
	socketsTTL = 500 * time.Millisecond
	// 两次列出的最短间隔：同时建立的一批连接共用一次列出。一次列出要几毫秒，间隔太短时反复连接的程序能占掉大量 CPU；
	// 太长则新连接等得久（规则匹配前要等归属结果），网页并发加载图片时能感觉到，所以不宜再加大。
	socketsMinRescan = 50 * time.Millisecond
	// 连续这么多次系统连接表查不到、列 socket 却查到别的进程，就认为系统连接表对我们无效，不再读它。
	pcblistGiveUp = 16
	socketsCap    = 65536
)

// nativeOwner 表示由 Velum 自己查询连接归属（见 platform.FindConnectionOwner）。
const nativeOwner = true

func findOwner(proto int, src netip.AddrPort, dst netip.AddrPort) (*adapter.ConnectionOwner, error) {
	network := "tcp"
	if proto == syscall.IPPROTO_UDP {
		network = "udp"
	}
	if pcblistMisses.Load() < pcblistGiveUp {
		if o, err := process.FindDarwinConnectionOwner(network, src, dst); err == nil && o.ProcessPath != "" {
			if int(o.ProcessID) != os.Getpid() { // 能看到自己不算：无效时也看得到
				pcblistMisses.Store(0)
			}
			return o, nil
		}
	}
	pid := socketOwner(portKey{proto, src.Port()})
	if pid == 0 {
		return nil, process.ErrNotFound
	}
	if pid != os.Getpid() {
		pcblistMisses.Add(1)
	}
	path, err := pidPath(pid)
	if err != nil {
		return nil, err
	}
	return &adapter.ConnectionOwner{ProcessID: uint32(pid), UserId: -1, ProcessPath: path}, nil
}

// socketOwner 返回占用本地端口的进程。缓存里没有时，以开始查询之后的一次列出为准：
// 距上次列出太近就等到可以再次列出（期间别的查询列出了也算），不会因为连接建立得密集而漏查。
func socketOwner(k portKey) int {
	asked := time.Now()
	for {
		sockets.Lock()
		age := time.Since(sockets.at)
		pid, ok := sockets.pids[k]
		switch {
		case ok && age < socketsTTL:
		case !sockets.at.Before(asked): // 查询开始后已经列出过：确实查不到（如 root 进程）
		case age >= socketsMinRescan:
			pid = rescanLocked(k)
		default:
			sockets.Unlock()
			time.Sleep(socketsMinRescan - age)
			continue
		}
		sockets.Unlock()
		return pid
	}
}

func rescanLocked(k portKey) int {
	at := time.Now()
	buf := make([]C.int, socketsCap*3)
	n := int(C.list_sockets(&buf[0], C.int(socketsCap)))
	pids := make(map[portKey]int, n)
	for i := 0; i < n; i++ {
		k := portKey{int(buf[i*3]), uint16(buf[i*3+1])}
		if _, ok := pids[k]; !ok {
			pids[k] = int(buf[i*3+2])
		}
	}
	sockets.pids, sockets.at = pids, at
	return pids[k]
}

func pidPath(pid int) (string, error) {
	buf := make([]C.char, C.PROC_PIDPATHINFO_MAXSIZE)
	if n := C.proc_pidpath(C.int(pid), unsafe.Pointer(&buf[0]), C.uint32_t(len(buf))); n <= 0 {
		return "", process.ErrNotFound
	}
	return C.GoString(&buf[0]), nil
}
