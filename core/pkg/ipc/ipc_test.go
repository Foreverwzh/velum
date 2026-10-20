package ipc_test

import (
	"context"
	"io"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/Foreverwzh/velum/core/pkg/ipc"
)

func TestListenDial(t *testing.T) {
	addr := filepath.Join(t.TempDir(), "t.sock")
	if runtime.GOOS == "windows" {
		addr = `\\.\pipe\velum-ipc-test-` + time.Now().Format("150405.000000")
	}
	if ipc.Alive(addr) {
		t.Fatal("监听前不应可连接")
	}
	ln, err := ipc.Listen(addr, ipc.ListenOptions{})
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		_, _ = c.Write([]byte("hi"))
		_ = c.Close()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	c, err := ipc.Dial(ctx, addr)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(c)
	_ = c.Close()
	if string(b) != "hi" {
		t.Fatalf("got %q", b)
	}
	_ = ln.Close()
	if ipc.Alive(addr) {
		t.Fatal("关闭后不应可连接")
	}
}
