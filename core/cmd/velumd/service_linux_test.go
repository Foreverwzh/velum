package main

import (
	"os/user"
	"strings"
	"testing"
)

func TestOwnUser(t *testing.T) {
	if !ownUser(&user.User{Username: "velum", HomeDir: "/var/lib/velum"}) {
		t.Error("服务用户应被识别为自己创建的")
	}
	if ownUser(&user.User{Username: "velum", HomeDir: "/home/velum"}) {
		t.Error("真人账号不应被识别为自己创建的")
	}
}

func TestUnitFile(t *testing.T) {
	u := unitFile()
	for _, want := range []string{
		"User=velum\n", "StartLimitIntervalSec=0\n", "Environment=VELUM_SERVICE=1\n", "ExecStart=/usr/local/bin/velumd --home /etc/velum --foreground\n",
		"AmbientCapabilities=CAP_NET_ADMIN CAP_NET_RAW CAP_NET_BIND_SERVICE\n", "ProtectSystem=strict\n", "ReadWritePaths=/etc/velum /var/lib/velum\n",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("单元文件缺少 %q", want)
		}
	}
}
