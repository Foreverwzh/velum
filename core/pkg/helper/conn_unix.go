//go:build unix

package helper

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"

	"golang.org/x/sys/unix"
)

// send 发送一条 JSON 消息；fd >= 0 时附带 SCM_RIGHTS。
func send(conn *net.UnixConn, v any, fd int) error {
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
		return fmt.Errorf("short write")
	}
	return nil
}

// recv 读取一条消息；若附带 fd 则返回，否则为 -1。请求 / 响应严格一问一答。
func recv(conn *net.UnixConn, v any) (int, error) {
	buf := make([]byte, 64*1024)
	oob := make([]byte, unix.CmsgSpace(4*4))
	n, oobn, _, _, err := conn.ReadMsgUnix(buf, oob)
	if err != nil {
		return -1, err
	}
	if n == 0 {
		return -1, errors.New("connection closed")
	}
	fd := -1
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
			for _, f := range fds {
				if fd < 0 {
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
		return -1, err
	}
	return fd, nil
}
