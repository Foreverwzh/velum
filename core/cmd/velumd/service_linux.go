package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Foreverwzh/velum/core/pkg/config"
	"github.com/Foreverwzh/velum/core/pkg/ipc"
	"github.com/Foreverwzh/velum/core/pkg/rpc"
)

// Linux 系统服务（docs/ML-PLAN.md、D7）：velumd 以专用用户 velum 运行，只带网络相关的 capabilities。
// 崩溃后一直重试（RestartSec 控制频率）：默认的启动频率限制会让服务在几次手动重启后不再被拉起（StartLimitIntervalSec=0）。
//
//   - install（需要 root）：创建用户与目录，把 velumd、vlu 复制到 /usr/local/bin，写入 systemd 单元，设为开机启动并（重新）启动。
//     重复执行就是升级：替换程序并重启，配置不变。
//   - uninstall（需要 root）：停止并删除服务与程序，默认保留配置和数据；--purge 连同配置、数据和用户一起删除。
//   - start / stop / status：systemctl 的薄封装。
//   - cleanup-net：清除 TUN 崩溃后的网络残留，由单元的 ExecStartPre / ExecStopPost 调用。
//   - tun-revert：TUN 试运行超时未确认时由定时器调用（root）。
const (
	serviceName = "velum"
	serviceUser = "velum"
	unitPath    = "/etc/systemd/system/velum.service"
	binDir      = "/usr/local/bin"
)

// revertMarker 由回退定时器（root）写入，velumd 读取后在 vlu status 里说明原因。
var revertMarker = filepath.Join(config.ServiceDataDir, "tun-reverted")

func unitFile() string {
	return `[Unit]
Description=Velum proxy
Documentation=https://github.com/Foreverwzh/velum
After=network-online.target
Wants=network-online.target
StartLimitIntervalSec=0

[Service]
Type=simple
User=` + serviceUser + `
Group=` + serviceUser + `
Environment=VELUM_SERVICE=1
Environment=HOME=` + config.ServiceDataDir + `
ExecStartPre=-` + binDir + `/velumd service cleanup-net
ExecStart=` + binDir + `/velumd --home ` + config.ServiceHome + ` --foreground
ExecStopPost=-` + binDir + `/velumd service cleanup-net
Restart=on-failure
RestartSec=3
TimeoutStopSec=15
LimitNOFILE=65535
RuntimeDirectory=velum
RuntimeDirectoryMode=0750
AmbientCapabilities=CAP_NET_ADMIN CAP_NET_RAW CAP_NET_BIND_SERVICE
CapabilityBoundingSet=CAP_NET_ADMIN CAP_NET_RAW CAP_NET_BIND_SERVICE
NoNewPrivileges=true
ProtectSystem=strict
ProtectHome=true
PrivateTmp=true
ReadWritePaths=` + config.ServiceHome + ` ` + config.ServiceDataDir + `
DeviceAllow=/dev/net/tun rw
RestrictAddressFamilies=AF_INET AF_INET6 AF_UNIX AF_NETLINK

[Install]
WantedBy=multi-user.target
`
}

func serviceCommand(args []string) error {
	const usage = "用法：velumd service install [--add-user <用户>] | uninstall [--purge] | start | stop | status | cleanup-net | tun-revert"
	if len(args) == 0 {
		return errors.New(usage)
	}
	fs := flag.NewFlagSet("service "+args[0], flag.ContinueOnError)
	addUser := fs.String("add-user", "", "允许该用户用 vlu 操作服务（加入 velum 组）；默认是执行 sudo 的用户")
	purge := fs.Bool("purge", false, "同时删除配置、数据和 velum 用户")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	switch args[0] {
	case "install":
		return serviceInstall(*addUser)
	case "uninstall":
		return serviceUninstall(*purge)
	case "cleanup-net":
		return cleanupNet()
	case "tun-revert":
		return tunRevert()
	case "start", "stop", "status":
		cmd := exec.Command("systemctl", args[0], "--no-pager", serviceName)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
		return cmd.Run()
	}
	return errors.New(usage)
}

func run(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s：%v\n%s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// tunRevert 由试运行的定时器调用：velumd 还在试运行或没有响应时，停止服务、清理网络后重新启动。
// 试运行不写入配置，重启后就回到关闭状态。不依赖 velumd 配合：它可能已经卡死。
func tunRevert() error {
	if trialOver(3 * time.Second) {
		fmt.Println("没有进行中的试运行，不需要回退")
		return nil
	}
	fmt.Println("TUN 试运行超时未确认，正在回退")
	if err := run("systemctl", "stop", serviceName); err != nil {
		fmt.Fprintln(os.Stderr, err) // 继续：网络恢复比服务状态重要
		_ = run("systemctl", "kill", "--signal=SIGKILL", serviceName)
	}
	cerr := cleanupNet()
	note := "TUN 试运行在 " + time.Now().Format("2006-01-02 15:04:05") + " 超时未确认，已自动回退到端口模式"
	if err := os.WriteFile(revertMarker, []byte(note+"\n"), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, err)
	}
	return errors.Join(cerr, run("systemctl", "start", serviceName))
}

// trialOver 询问 velumd 试运行是否已结束；velumd 没有响应（可能已卡死）时返回 false。
// 不能等 RPC 自身的超时：TUN 卡死时机器一直断网，每一秒都算数。
func trialOver(wait time.Duration) bool {
	over := make(chan bool, 1)
	go func() {
		s, err := rpc.NewClient(config.ServiceSocket).Status()
		over <- err == nil && s.TUNTrial == nil
	}()
	select {
	case v := <-over:
		return v
	case <-time.After(wait):
		return false
	}
}

// ownUser 判断用户是否由 serviceInstall 创建：主目录是服务的数据目录。
func ownUser(u *user.User) bool { return u.HomeDir == config.ServiceDataDir }

func requireRootSystemd() error {
	if os.Geteuid() != 0 {
		return errors.New("需要 root（用 sudo 运行）")
	}
	if _, err := os.Stat("/run/systemd/system"); err != nil {
		return errors.New("这台机器没有运行 systemd；可以用 velumd --foreground 直接运行")
	}
	return nil
}

func serviceInstall(addUser string) error {
	if err := requireRootSystemd(); err != nil {
		return err
	}
	if addUser == "" {
		if u := os.Getenv("SUDO_USER"); u != "" && u != "root" {
			addUser = u
		}
	}
	if addUser != "" {
		if _, err := user.Lookup(addUser); err != nil { // 先检查，不留下装了一半的状态
			return fmt.Errorf("用户 %s 不存在", addUser)
		}
	}
	// 同名用户不是我们创建的（如真人账号）时不能借用：服务会以它的身份运行，--purge 还会删掉它
	if u, err := user.Lookup(serviceUser); err == nil && !ownUser(u) {
		return fmt.Errorf("系统里已有用户 %s（主目录 %s），不是 Velum 创建的；为避免影响它，已停止安装", serviceUser, u.HomeDir)
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if exe, err = filepath.EvalSymlinks(exe); err != nil {
		return err
	}
	vlu := filepath.Join(filepath.Dir(exe), "vlu")
	if _, err := os.Stat(vlu); err != nil {
		return fmt.Errorf("没有找到 %s：velumd 和 vlu 需要放在同一目录", vlu)
	}

	u, err := user.Lookup(serviceUser)
	if err != nil {
		group := "--user-group"
		if _, err := user.LookupGroup(serviceUser); err == nil {
			group = "--gid=" + serviceUser // 组已存在（如管理员预先创建）：直接使用
		}
		if err := run("useradd", "--system", group, "--home-dir", config.ServiceDataDir, "--no-create-home", "--shell", "/usr/sbin/nologin", serviceUser); err != nil {
			return err
		}
		if u, err = user.Lookup(serviceUser); err != nil {
			return err
		}
	}
	uid, _ := strconv.Atoi(u.Uid)
	gid, _ := strconv.Atoi(u.Gid)
	for _, d := range []string{config.ServiceHome, config.ServiceDataDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
		if err := os.Chmod(d, 0o700); err != nil {
			return err
		}
		if err := os.Chown(d, uid, gid); err != nil {
			return err
		}
	}
	if err := installBin(exe, filepath.Join(binDir, "velumd")); err != nil {
		return err
	}
	if err := installBin(vlu, filepath.Join(binDir, "vlu")); err != nil {
		return err
	}
	if err := os.WriteFile(unitPath, []byte(unitFile()), 0o644); err != nil {
		return err
	}
	if addUser != "" {
		if err := run("usermod", "-aG", serviceUser, addUser); err != nil {
			return err
		}
	}
	for _, c := range [][]string{{"daemon-reload"}, {"enable", serviceName}, {"restart", serviceName}} {
		if err := run("systemctl", c...); err != nil {
			return err
		}
	}
	// 等本地接口就绪：否则紧接着执行 vlu 会被告知 velumd 未运行
	for i := 0; i < 100 && !ipc.Alive(config.ServiceSocket); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	if !ipc.Alive(config.ServiceSocket) {
		return fmt.Errorf("服务已启动，但 10 秒内本地接口没有就绪；查看日志：journalctl -u %s", serviceName)
	}
	fmt.Println("velum 服务已安装并启动（开机自动运行）")
	fmt.Printf("  配置 %s，数据 %s，日志 journalctl -u %s\n", config.ServiceHome, config.ServiceDataDir, serviceName)
	if addUser != "" {
		fmt.Printf("  已把 %s 加入 velum 组，重新登录后可直接用 vlu；现在可以用 sudo vlu\n", addUser)
	}
	fmt.Println("  下一步：vlu sub <订阅地址>")
	return nil
}

// installBin 原子替换：运行中的旧程序不受影响，也不会有写到一半的文件。
func installBin(src, dst string) error {
	if s, err := filepath.EvalSymlinks(dst); err == nil && s == src {
		return nil // 从安装位置重新运行
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	tmp, err := os.CreateTemp(filepath.Dir(dst), "."+filepath.Base(dst)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := io.Copy(tmp, in); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	if err := os.Chmod(tmp.Name(), 0o755); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), dst)
}

func serviceUninstall(purge bool) error {
	if err := requireRootSystemd(); err != nil {
		return err
	}
	if _, err := os.Stat(unitPath); err == nil {
		_ = run("systemctl", "disable", "--now", serviceName)
		if err := os.Remove(unitPath); err != nil {
			return err
		}
		if err := run("systemctl", "daemon-reload"); err != nil {
			return err
		}
	}
	for _, b := range []string{"velumd", "vlu"} {
		if err := os.Remove(filepath.Join(binDir, b)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	if !purge {
		fmt.Printf("velum 服务已卸载；配置和数据保留在 %s、%s（--purge 可一并删除）\n", config.ServiceHome, config.ServiceDataDir)
		return nil
	}
	for _, d := range []string{config.ServiceHome, config.ServiceDataDir} {
		if err := os.RemoveAll(d); err != nil {
			return err
		}
	}
	u, err := user.Lookup(serviceUser)
	if err == nil && !ownUser(u) {
		fmt.Printf("velum 服务已卸载，配置和数据已删除；用户 %s 不是 Velum 创建的，已保留\n", serviceUser)
		return nil
	}
	if err == nil {
		if err := run("userdel", serviceUser); err != nil {
			return err
		}
		// 组里还有其他成员（--add-user）时 userdel 不会删组
		if _, err := user.LookupGroup(serviceUser); err == nil {
			if err := run("groupdel", serviceUser); err != nil {
				return err
			}
		}
	}
	fmt.Println("velum 服务已卸载，配置、数据和 velum 用户已删除")
	return nil
}
