//go:build linux && !android

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Foreverwzh/velum/core/pkg/rpc"
)

// TUN 试运行（docs/ML-PLAN.md）：防止把人锁在服务器外面。回退定时器由 systemd 执行，不依赖 velumd。
const (
	revertUnit      = "velum-tun-revert"
	defaultTrial    = 120 * time.Second
	minTrial        = 30 * time.Second
	installedVelumd = "/usr/local/bin/velumd"
)

type tunArgs struct {
	action string
	try    bool
	trial  time.Duration
	force  bool
}

const tunUsage = "用法：vlu tun on [--try[=120s]] [--force] | confirm [--force] | off | config"

func parseTUNArgs(args []string) (tunArgs, error) {
	a := tunArgs{trial: defaultTrial}
	if len(args) == 0 {
		return a, errors.New(tunUsage)
	}
	a.action = args[0]
	for i := 1; i < len(args); i++ {
		v := args[i]
		dur := ""
		switch {
		case v == "--force":
			a.force = true
			continue
		case v == "--try":
			a.try = true
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "-") {
				i++
				dur = args[i]
			}
		case strings.HasPrefix(v, "--try="):
			a.try = true
			dur = strings.TrimPrefix(v, "--try=")
		default:
			return a, errors.New(tunUsage)
		}
		if dur != "" {
			d, err := time.ParseDuration(dur)
			if err != nil {
				return a, fmt.Errorf("试运行时长无效：%s（例如 120s）", dur)
			}
			if d < minTrial {
				return a, fmt.Errorf("试运行时长不能少于 %s：要留出新开 SSH 连接确认的时间", minTrial)
			}
			a.trial = d
		}
	}
	switch a.action {
	case "on", "off", "config", "confirm":
	default:
		return a, errors.New(tunUsage)
	}
	if a.try && a.action != "on" {
		return a, errors.New(tunUsage)
	}
	return a, nil
}

// sshSession 沿父进程链找到这条 SSH 连接对应的 sshd 进程，返回其 PID；不在 SSH 里时返回空。
// 用进程链而不是 SSH_CONNECTION 环境变量：后者经 sudo 通常被清除。
func sshSession() string { return sshSessionFrom(os.Getpid(), "/proc") }

func sshSessionFrom(pid int, proc string) string {
	for i := 0; i < 64 && pid > 1; i++ {
		b, err := os.ReadFile(filepath.Join(proc, strconv.Itoa(pid), "stat"))
		if err != nil {
			return ""
		}
		// 格式：pid (comm) state ppid …，comm 可能含空格和括号
		s := string(b)
		l, r := strings.IndexByte(s, '('), strings.LastIndexByte(s, ')')
		if l < 0 || r < l {
			return ""
		}
		if comm := s[l+1 : r]; comm == "sshd" || comm == "sshd-session" { // OpenSSH 9.8 起每条连接是 sshd-session
			return strconv.Itoa(pid)
		}
		f := strings.Fields(s[r+1:])
		if len(f) < 2 {
			return ""
		}
		if pid, err = strconv.Atoi(f[1]); err != nil {
			return ""
		}
	}
	return ""
}

func stopRevertTimer() {
	_ = exec.Command("systemctl", "stop", revertUnit+".timer").Run()
	_ = exec.Command("systemctl", "reset-failed", revertUnit+".service", revertUnit+".timer").Run()
}

func setTUN(c *rpc.Client, args []string) error {
	a, err := parseTUNArgs(args)
	if err != nil {
		return err
	}
	_, serr := os.Stat(linuxServiceUnit)
	installed := serr == nil
	switch {
	case a.try:
		if !installed {
			return errors.New("试运行需要系统服务：先执行 sudo velumd service install")
		}
		return tunTry(c, a.trial)
	case a.action == "confirm":
		return tunConfirm(c, a.force)
	case (a.action == "on" || a.action == "config") && installed && !a.force && sshSession() != "":
		// config 也可能开启 TUN（tun.enabled: true）
		return fmt.Errorf("当前在 SSH 会话里，直接接管网络可能把你锁在服务器外面。请用 sudo vlu tun on --try，确认后才生效；确实要跳过时加 --force")
	}
	err = setSwitch(c, "TUN", []string{a.action})
	if err == nil && os.Geteuid() == 0 && installed {
		stopRevertTimer() // 开关已由用户明确决定；没有 root 时定时器到期发现没有试运行，什么也不做
	}
	return err
}

func tunTry(c *rpc.Client, d time.Duration) error {
	if os.Geteuid() != 0 {
		return errors.New("试运行要注册 systemd 定时器，需要 root：sudo vlu tun on --try")
	}
	s, err := c.Status()
	if err != nil {
		return err
	}
	if s.TUNOn {
		return errors.New("TUN 已开启，不需要试运行")
	}
	// 先注册回退，再动网络：动网络之后这条 SSH 连接可能已经断了
	stopRevertTimer()
	secs := strconv.Itoa(int(d.Seconds())) + "s"
	if out, err := exec.Command("systemd-run", "--quiet", "--unit="+revertUnit, "--collect", "--on-active="+secs, installedVelumd, "service", "tun-revert").CombinedOutput(); err != nil {
		return fmt.Errorf("注册回退定时器失败，没有开启 TUN：%v\n%s", err, strings.TrimSpace(string(out)))
	}
	s, err = c.Switch(rpc.SwitchRequest{TUN: "try", TrialDeadline: time.Now().Add(d), TrialSession: sshSession()})
	switch {
	case err != nil:
		stopRevertTimer()
		return err
	case s.LastError != "":
		stopRevertTimer()
		return errors.New(s.LastError)
	case !s.TUNOn:
		stopRevertTimer()
		if s.Network != nil && s.Network.Paused != "" {
			return fmt.Errorf("TUN 没有开启：%s", s.Network.Paused)
		}
		return errors.New("TUN 没有开启")
	}
	fmt.Printf("TUN 试运行中：%s 内未确认就自动回退到端口模式，重启后也会回到端口模式\n", d)
	if s.TUN != nil {
		fmt.Printf("  %s %s，%d 条路由\n", s.TUN.Name, s.TUN.Address, len(s.TUN.Routes))
	}
	fmt.Println("  现在**新开一个 SSH 连接**执行：sudo vlu tun confirm")
	return nil
}

func tunConfirm(c *rpc.Client, force bool) error {
	if os.Geteuid() != 0 {
		return errors.New("确认要取消 systemd 定时器，需要 root：sudo vlu tun confirm")
	}
	// 先保存，再取消定时器：定时器若在两步之间到期，发现试运行已结束，不会回退
	s, err := c.Switch(rpc.SwitchRequest{TUN: "confirm", TrialSession: sshSession(), TrialForce: force})
	if err != nil {
		return err
	}
	stopRevertTimer()
	if s.LastError != "" {
		return errors.New(s.LastError)
	}
	fmt.Println("TUN 已确认并保存，重启后保持开启")
	return nil
}
