//go:build darwin

package daemon_test

import (
	"context"
	"io"
	"log"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Foreverwzh/velum/core/pkg/config"
	"github.com/Foreverwzh/velum/core/pkg/daemon"
	"github.com/Foreverwzh/velum/core/pkg/helper"
	"github.com/Foreverwzh/velum/core/pkg/rpc"
)

// /v1/helper：helper 不在时给出原因；在运行时可达；拒绝其他 uid 时不可达。
func TestHelperStatus(t *testing.T) {
	home, _ := os.MkdirTemp("/tmp", "velum-test-")
	defer os.RemoveAll(home)
	paths := testPaths(home)
	_ = config.Ensure(paths)
	hsock := filepath.Join(home, "h.sock")
	cfg := strings.Replace(config.Template, "helper_socket: /var/run/velum-helper.sock", "helper_socket: "+hsock, 1)
	_ = os.WriteFile(paths.Config, []byte(cfg), 0o600)
	d := daemon.New(paths, &fakeEngine{}, log.New(io.Discard, "", 0))
	go func() { _ = d.Run(context.Background()) }()
	defer d.Shutdown()
	c := rpc.NewClient(paths.Socket)
	for i := 0; i < 100; i++ {
		if _, err := c.Status(); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	hs, err := c.Helper()
	if err != nil || hs.Reachable || hs.Error == "" || hs.Socket != hsock {
		t.Fatalf("helper 不存在时：%v %+v", err, hs)
	}
	for _, tc := range []struct {
		uid  int
		want bool
	}{{os.Getuid(), true}, {os.Getuid() + 1, false}} {
		_ = os.Remove(hsock)
		ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: hsock, Net: "unix"})
		if err != nil {
			t.Fatal(err)
		}
		srv := &helper.Server{AllowUID: tc.uid, Logger: log.New(io.Discard, "", 0)}
		go func() { _ = srv.Serve(ln) }()
		hs, err = c.Helper()
		ln.Close()
		srv.Shutdown("test")
		if err != nil || hs.Reachable != tc.want {
			t.Fatalf("uid=%d：%v %+v", tc.uid, err, hs)
		}
	}
}
