// Package proto 定义 velumd ↔ velum-helper 的最小 UDS 协议（spike 用）。
//
// 帧格式：请求为单行 JSON；响应为单条 sendmsg，正文是单行 JSON，
// open_tun 成功时通过 SCM_RIGHTS 附带 utun 文件描述符。
// 请求/响应严格一问一答，客户端在收到响应前不会发下一个请求。
package proto

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

const (
	OpOpenTun  = "open_tun"
	OpAddRoute = "add_route"
	OpDelRoute = "del_route"
	// OpFindOwner：macOS 上非 root 进程用 pcblist_n 只能看到自己的 socket，
	// 进程归属查询必须由 root helper 代查。
	OpFindOwner = "find_owner"
)

type Request struct {
	Op     string   `json:"op"`
	MTU    uint32   `json:"mtu,omitempty"`
	Inet4  []string `json:"inet4,omitempty"`  // 例如 "172.19.0.1/30"
	Routes []string `json:"routes,omitempty"` // 例如 "223.6.6.6/32"

	Network string `json:"network,omitempty"` // tcp / udp
	Src     string `json:"src,omitempty"`     // ip:port
	Dst     string `json:"dst,omitempty"`     // ip:port
}

type Response struct {
	OK    bool   `json:"ok"`
	Error string `json:"error,omitempty"`
	Name  string `json:"name,omitempty"` // utun 名称

	PID  uint32 `json:"pid,omitempty"`
	UID  int32  `json:"uid,omitempty"`
	Path string `json:"path,omitempty"`
}

// Send 发送一条 JSON 消息，fd >= 0 时附带 SCM_RIGHTS。
func Send(conn *net.UnixConn, v any, fd int) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	data = append(data, '\n')
	var oob []byte
	if fd >= 0 {
		oob = unix.UnixRights(fd)
	}
	n, oobn, err := conn.WriteMsgUnix(data, oob, nil)
	if err != nil {
		return err
	}
	if n != len(data) || oobn != len(oob) {
		return fmt.Errorf("short write: %d/%d oob %d/%d", n, len(data), oobn, len(oob))
	}
	return nil
}

// Recv 读取一条消息；若附带了 fd 则返回，否则 fd = -1。
func Recv(conn *net.UnixConn, v any) (fd int, err error) {
	buf := make([]byte, 64*1024)
	oob := make([]byte, unix.CmsgSpace(4*4))
	n, oobn, _, _, err := conn.ReadMsgUnix(buf, oob)
	if err != nil {
		return -1, err
	}
	if n == 0 {
		return -1, errors.New("connection closed")
	}
	fd = -1
	if oobn > 0 {
		msgs, err := unix.ParseSocketControlMessage(oob[:oobn])
		if err != nil {
			return -1, err
		}
		for _, m := range msgs {
			fds, err := unix.ParseUnixRights(&m)
			if err != nil {
				continue
			}
			for i, f := range fds {
				if i == 0 && fd < 0 {
					fd = f
				} else {
					unix.Close(f)
				}
			}
		}
	}
	if err := json.Unmarshal(buf[:n], v); err != nil {
		if fd >= 0 {
			unix.Close(fd)
		}
		return -1, fmt.Errorf("decode response: %w", err)
	}
	return fd, nil
}
