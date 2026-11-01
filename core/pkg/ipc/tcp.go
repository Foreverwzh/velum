package ipc

import (
	"bufio"
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"
)

// TCPPrefix 是回环 TCP 地址的前缀：tcp://127.0.0.1:端口?token=…
const TCPPrefix = "tcp://"

// tcpAddr 是解析后的回环 TCP 地址。
type tcpAddr struct {
	hostport, token string
}

// TCPAddr 组成回环 TCP 地址。
func TCPAddr(hostport, token string) string {
	return TCPPrefix + hostport + "?token=" + url.QueryEscape(token)
}

// parseTCP 解析回环 TCP 地址；ok 表示 addr 是 tcp:// 形式。只允许回环地址与非空令牌：令牌是唯一的访问控制。
func parseTCP(addr string) (t tcpAddr, ok bool, err error) {
	if !strings.HasPrefix(addr, TCPPrefix) {
		return t, false, nil
	}
	u, err := url.Parse(addr)
	if err != nil {
		return t, true, fmt.Errorf("本地接口地址无效：%w", err)
	}
	ap, err := netip.ParseAddrPort(u.Host)
	if err != nil || !ap.Addr().IsLoopback() {
		return t, true, fmt.Errorf("本地接口只能监听回环地址：%s", u.Host)
	}
	t = tcpAddr{hostport: u.Host, token: u.Query().Get("token")}
	if t.token == "" {
		return t, true, errors.New("本地接口地址缺少令牌")
	}
	return t, true, nil
}

// 连接建立后客户端先发送一行令牌（以 \n 结尾），服务端校验通过后才交给上层（HTTP）。
const handshakeTimeout = 3 * time.Second

func dialTCP(ctx context.Context, t tcpAddr) (net.Conn, error) {
	var d net.Dialer
	c, err := d.DialContext(ctx, "tcp", t.hostport)
	if err != nil {
		return nil, err
	}
	_ = c.SetWriteDeadline(time.Now().Add(handshakeTimeout))
	if _, err := c.Write([]byte(t.token + "\n")); err != nil {
		c.Close()
		return nil, err
	}
	_ = c.SetWriteDeadline(time.Time{})
	return c, nil
}

func listenTCP(t tcpAddr) (net.Listener, error) {
	ln, err := net.Listen("tcp", t.hostport)
	if err != nil {
		return nil, err
	}
	l := &tokenListener{Listener: ln, token: t.token, conns: make(chan net.Conn), done: make(chan struct{})}
	go l.loop()
	return l, nil
}

// tokenListener 只交出发送了正确令牌的连接；令牌错误或超时的连接直接关闭。
// 每个连接在自己的 goroutine 里校验，慢速或恶意的连接不会挡住其他连接。
type tokenListener struct {
	net.Listener
	token     string
	conns     chan net.Conn
	done      chan struct{}
	closeOnce sync.Once
}

func (l *tokenListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}

func (l *tokenListener) Close() error {
	l.closeOnce.Do(func() { close(l.done) })
	return l.Listener.Close()
}

func (l *tokenListener) loop() {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				l.closeOnce.Do(func() { close(l.done) })
				return
			}
			continue
		}
		go func() {
			bc, ok := l.verify(c)
			if !ok {
				return
			}
			select {
			case l.conns <- bc:
			case <-l.done:
				bc.Close()
			}
		}()
	}
}

func (l *tokenListener) verify(c net.Conn) (net.Conn, bool) {
	_ = c.SetReadDeadline(time.Now().Add(handshakeTimeout))
	r := bufio.NewReaderSize(c, 256)
	line, err := r.ReadSlice('\n')
	if err != nil || subtle.ConstantTimeCompare([]byte(strings.TrimSuffix(string(line), "\n")), []byte(l.token)) != 1 {
		c.Close()
		return nil, false
	}
	_ = c.SetReadDeadline(time.Time{})
	return &bufferedConn{Conn: c, r: r}, true
}

// bufferedConn 把握手时多读入缓冲区的数据（客户端紧接着发送的 HTTP 请求）交还给上层。
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (c *bufferedConn) Read(p []byte) (int, error) { return c.r.Read(p) }
