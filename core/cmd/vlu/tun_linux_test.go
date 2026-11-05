//go:build linux && !android

package main

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

func TestParseTUNArgs(t *testing.T) {
	for _, c := range []struct {
		args  []string
		try   bool
		trial time.Duration
		force bool
		bad   bool
	}{
		{args: []string{"on"}, trial: defaultTrial},
		{args: []string{"on", "--try"}, try: true, trial: defaultTrial},
		{args: []string{"on", "--try", "60s"}, try: true, trial: 60 * time.Second},
		{args: []string{"on", "--try=5m"}, try: true, trial: 5 * time.Minute},
		{args: []string{"on", "--try", "--force"}, try: true, trial: defaultTrial, force: true},
		{args: []string{"confirm", "--force"}, trial: defaultTrial, force: true},
		{args: []string{"on", "--try=5s"}, bad: true},
		{args: []string{"on", "--try=abc"}, bad: true},
		{args: []string{"off", "--try"}, bad: true},
		{args: []string{"status"}, bad: true},
		{args: nil, bad: true},
	} {
		a, err := parseTUNArgs(c.args)
		if (err != nil) != c.bad {
			t.Errorf("%v：err = %v", c.args, err)
			continue
		}
		if !c.bad && (a.try != c.try || a.trial != c.trial || a.force != c.force) {
			t.Errorf("%v：得到 %+v", c.args, a)
		}
	}
}

func TestSSHSession(t *testing.T) {
	proc := t.TempDir()
	stat := func(pid, ppid int, comm string) {
		d := filepath.Join(proc, strconv.Itoa(pid))
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		s := strconv.Itoa(pid) + " (" + comm + ") S " + strconv.Itoa(ppid) + " 0 0\n"
		if err := os.WriteFile(filepath.Join(d, "stat"), []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	stat(100, 1, "systemd")
	stat(200, 100, "sshd")
	stat(201, 200, "sshd-session")
	stat(300, 201, "bash")
	stat(301, 300, "my (odd) sudo")
	stat(400, 100, "login")
	stat(401, 400, "bash")
	if got := sshSessionFrom(301, proc); got != "201" {
		t.Errorf("SSH 会话：得到 %q，应为 201（离自己最近的 sshd）", got)
	}
	if got := sshSessionFrom(401, proc); got != "" {
		t.Errorf("本地控制台：得到 %q，应为空", got)
	}
}
