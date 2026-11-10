package daemon_test

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Foreverwzh/velum/core/pkg/daemon"
	"github.com/Foreverwzh/velum/core/pkg/rpc"
)

func startTrialDaemon(t *testing.T, home string, setup func(*daemon.Daemon)) (*rpc.Client, *fakeEngine, func()) {
	t.Helper()
	paths := testPaths(home)
	eng := &fakeEngine{}
	d := newDaemon(paths, eng, log.New(io.Discard, "", 0))
	d.TUNOpener = &fakeOpener{}
	if setup != nil {
		setup(d)
	}
	done := make(chan struct{})
	go func() { _ = d.Run(context.Background()); close(done) }()
	c := rpc.NewClient(paths.Socket)
	for i := 0; i < 200; i++ {
		if s, err := c.Status(); err == nil && !s.Starting {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	return c, eng, func() { d.Shutdown(); <-done }
}

// 试运行不持久化：重启后回到关闭。这是「不把人锁在外面」的兜底，回退定时器只是重启服务。
func TestTUNTrialNotPersisted(t *testing.T) {
	home, _ := os.MkdirTemp(tmpBase(), "velum-test-")
	defer os.RemoveAll(home)
	on := func(d *daemon.Daemon) { d.TUNTrials = true }
	c, eng, stop := startTrialDaemon(t, home, on)
	s, err := c.Switch(rpc.SwitchRequest{TUN: "try", TrialDeadline: time.Now().Add(time.Minute), TrialSession: "1"})
	if err != nil || !s.TUNOn || s.TUNTrial == nil || eng.last.TUN == nil {
		t.Fatalf("开始试运行：%v %+v", err, s)
	}
	if _, err := c.Switch(rpc.SwitchRequest{TUN: "try"}); err == nil {
		t.Fatal("试运行中不应再次试运行")
	}
	// 重新加载配置不结束试运行
	if s, err = c.Reload(); err != nil || !s.TUNOn {
		t.Fatalf("reload：%v %+v", err, s)
	}
	stop()

	c, eng, stop = startTrialDaemon(t, home, on)
	defer stop()
	if s, err = c.Status(); err != nil || s.TUNOn || s.WantTUN || s.TUNTrial != nil || eng.last.TUN != nil {
		t.Fatalf("重启后应回到关闭：%v %+v", err, s)
	}
}

func TestTUNTrialConfirm(t *testing.T) {
	home, _ := os.MkdirTemp(tmpBase(), "velum-test-")
	defer os.RemoveAll(home)
	on := func(d *daemon.Daemon) { d.TUNTrials = true }
	c, _, stop := startTrialDaemon(t, home, on)
	if _, err := c.Switch(rpc.SwitchRequest{TUN: "confirm", TrialSession: "2"}); err == nil {
		t.Fatal("没有试运行时不能确认")
	}
	if _, err := c.Switch(rpc.SwitchRequest{TUN: "try", TrialSession: "1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := c.Switch(rpc.SwitchRequest{TUN: "confirm", TrialSession: "1"}); err == nil || !strings.Contains(err.Error(), "新开") {
		t.Fatalf("同一条连接不能确认：%v", err)
	}
	s, err := c.Switch(rpc.SwitchRequest{TUN: "confirm", TrialSession: "2"})
	if err != nil || !s.TUNOn || s.TUNTrial != nil {
		t.Fatalf("确认：%v %+v", err, s)
	}
	if _, err := c.Switch(rpc.SwitchRequest{TUN: "try"}); err == nil {
		t.Fatal("已开启时不应试运行")
	}
	stop()
	c, _, stop = startTrialDaemon(t, home, on)
	defer stop()
	if s, err = c.Status(); err != nil || !s.TUNOn {
		t.Fatalf("确认后重启应保持开启：%v %+v", err, s)
	}
}

// 同一条连接加 --force 可以确认；控制台上（没有 SSH 连接）也可以。
func TestTUNTrialConfirmForceAndConsole(t *testing.T) {
	home, _ := os.MkdirTemp(tmpBase(), "velum-test-")
	defer os.RemoveAll(home)
	c, _, stop := startTrialDaemon(t, home, func(d *daemon.Daemon) { d.TUNTrials = true })
	defer stop()
	for _, r := range []rpc.SwitchRequest{{TrialSession: "1", TrialForce: true}, {}} {
		tr := rpc.SwitchRequest{TUN: "try", TrialSession: r.TrialSession}
		if _, err := c.Switch(tr); err != nil {
			t.Fatal(err)
		}
		r.TUN = "confirm"
		if _, err := c.Switch(r); err != nil {
			t.Fatalf("%+v：%v", r, err)
		}
		if _, err := c.Switch(rpc.SwitchRequest{TUN: "off"}); err != nil {
			t.Fatal(err)
		}
	}
}

func TestTUNTrialOffAndUnsupported(t *testing.T) {
	home, _ := os.MkdirTemp(tmpBase(), "velum-test-")
	defer os.RemoveAll(home)
	c, _, stop := startTrialDaemon(t, home, nil)
	if _, err := c.Switch(rpc.SwitchRequest{TUN: "try"}); err == nil {
		t.Fatal("未启用试运行的平台应拒绝")
	}
	stop()
	marker := filepath.Join(home, "tun-reverted")
	_ = os.WriteFile(marker, []byte("已回退\n"), 0o644)
	c, _, stop = startTrialDaemon(t, home, func(d *daemon.Daemon) { d.TUNTrials, d.TUNRevertMarker = true, marker })
	defer stop()
	s, err := c.Status()
	if err != nil || s.TUNReverted != "已回退" {
		t.Fatalf("应显示回退原因：%v %+v", err, s)
	}
	if _, err := c.Switch(rpc.SwitchRequest{TUN: "try"}); err != nil {
		t.Fatal(err)
	}
	if s, err = c.Status(); err != nil || s.TUNReverted != "" {
		t.Fatalf("重新选择后不再显示回退原因：%v %+v", err, s)
	}
	if s, err = c.Switch(rpc.SwitchRequest{TUN: "off"}); err != nil || s.TUNOn || s.TUNTrial != nil {
		t.Fatalf("off 应结束试运行：%v %+v", err, s)
	}
}
