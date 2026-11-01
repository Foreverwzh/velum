package ipc_test

import (
	"context"
	"io"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/Foreverwzh/velum/core/pkg/ipc"
)

func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	return ln.Addr().String()
}

func TestTCPToken(t *testing.T) {
	hp := freeAddr(t)
	addr := ipc.TCPAddr(hp, "s3cret&x")
	ln, err := ipc.Listen(addr, ipc.ListenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			b := make([]byte, 4)
			n, _ := io.ReadFull(c, b)
			_, _ = c.Write([]byte("echo:" + string(b[:n])))
			_ = c.Close()
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// 正确令牌：握手后紧接着发送的数据原样交给上层
	c, err := ipc.Dial(ctx, addr)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = c.Write([]byte("ping"))
	b, _ := io.ReadAll(c)
	c.Close()
	if string(b) != "echo:ping" {
		t.Fatalf("got %q", b)
	}

	// 错误令牌：连接被关闭，上层收不到
	c, err = ipc.Dial(ctx, ipc.TCPAddr(hp, "wrong"))
	if err != nil {
		t.Fatal(err)
	}
	_, _ = c.Write([]byte("ping"))
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	b, _ = io.ReadAll(c)
	c.Close()
	if len(b) != 0 {
		t.Fatalf("错误令牌不应得到应答：%q", b)
	}

	if !ipc.Alive(addr) {
		t.Fatal("应可连接")
	}
	ln.Close()
	if ipc.Alive(addr) {
		t.Fatal("关闭后不应可连接")
	}
}

func TestTCPAddrRules(t *testing.T) {
	for _, a := range []string{"tcp://0.0.0.0:7000?token=x", "tcp://192.168.1.2:7000?token=x", "tcp://127.0.0.1:7000", "tcp://127.0.0.1:7000?token="} {
		if _, err := ipc.Listen(a, ipc.ListenOptions{}); err == nil || !strings.Contains(err.Error(), "本地接口") {
			t.Errorf("%s 应被拒绝，得到 %v", a, err)
		}
	}
}
