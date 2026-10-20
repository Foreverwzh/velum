package main

import (
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/Foreverwzh/velum/core/pkg/config"
	"github.com/Foreverwzh/velum/core/pkg/ipc"
	"github.com/Foreverwzh/velum/core/pkg/rpc"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/svc"
	"golang.org/x/sys/windows/svc/mgr"
)

// Windows 服务（ARCHITECTURE §3.3 / D5）：velumd 以 LocalSystem 运行，直接创建 wintun。
//
//   - install（需要管理员，App 经 UAC 调用）：把自身复制到 %ProgramFiles%\Velum\velumd.exe（只有管理员可写，
//     避免用户目录中的可执行文件被替换后以 SYSTEM 运行），注册手动启动的服务并立即启动；
//     服务 DACL 允许安装用户启动 / 停止（App 启动时拉起、退出时停止，与 macOS 一致）；失败自动重启。
//   - 服务的数据（日志、缓存、状态、快照）在 %ProgramData%\Velum（只有 SYSTEM / 管理员可写）；
//     用户目录中的 config.yaml / overlay.yaml 以用户身份读写（userfs + winuser.Impersonate）。
//   - uninstall（需要管理员）：停止并删除服务与复制的程序。
//   - start / stop / status：安装用户即可执行。
const serviceName = "Velum"

func installedExe() (string, error) {
	pf, err := windows.KnownFolderPath(windows.FOLDERID_ProgramFiles, 0)
	if err != nil {
		return "", err
	}
	return filepath.Join(pf, "Velum", "velumd.exe"), nil
}

func serviceCommand(args []string) (err error) {
	if len(args) == 0 {
		return errors.New("用法：velumd service install --home <目录> --allow-sid <SID> | uninstall | start | stop | status")
	}
	fs := flag.NewFlagSet("service "+args[0], flag.ContinueOnError)
	home := fs.String("home", "", "Velum 目录（安装用户的 %APPDATA%\\Velum）")
	sid := fs.String("allow-sid", "", "安装用户的 SID")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	// 经 UAC 提权运行时 App 拿不到 stderr：失败原因写入 %ProgramData%\Velum\logs\service.log 供 App 显示
	// （先确保数据目录安全，不向用户可控的位置写文件）
	if args[0] == "install" || args[0] == "uninstall" {
		defer func() {
			if err == nil {
				return
			}
			dir, derr := config.ServiceDataDir()
			if derr != nil || secureDataDir(dir, nonEmpty(*sid)) != nil {
				return
			}
			_ = os.MkdirAll(filepath.Join(dir, "logs"), 0o700)
			_ = os.WriteFile(filepath.Join(dir, "logs", "service.log"), []byte(err.Error()+"\n"), 0o600)
		}()
	}
	switch args[0] {
	case "install":
		return installService(*home, *sid)
	case "uninstall":
		return uninstallService()
	case "start":
		return controlService(true)
	case "stop":
		return controlService(false)
	case "status":
		st := serviceStatus()
		return json.NewEncoder(os.Stdout).Encode(st)
	}
	return fmt.Errorf("未知操作：%s", args[0])
}

func installService(home, sid string) error {
	if home == "" || !filepath.IsAbs(home) {
		return errors.New("需要 --home（绝对路径）：服务以 SYSTEM 运行，需要知道安装用户的 Velum 目录")
	}
	if _, err := windows.StringToSid(sid); err != nil {
		return fmt.Errorf("需要有效的 --allow-sid：%w", err)
	}
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("连接服务管理器（需要管理员权限）：%w", err)
	}
	defer m.Disconnect()
	// 释放程序文件与 Named Pipe：停止已有服务与以用户身份运行的 velumd（它们退出时会还原系统代理）
	if s, err := m.OpenService(serviceName); err == nil {
		err = stopAndWait(s)
		s.Close()
		if err != nil {
			return err
		}
	}
	if userPipe := config.UserPipe(sid); ipc.Alive(userPipe) {
		_ = rpc.NewClient(userPipe).Stop()
		for i := 0; i < 100 && ipc.Alive(userPipe); i++ {
			time.Sleep(100 * time.Millisecond)
		}
	}
	dataDir, err := config.ServiceDataDir()
	if err != nil {
		return err
	}
	if err := secureDataDir(dataDir, []string{sid}); err != nil {
		return err
	}
	exe, err := installedExe()
	if err != nil {
		return err
	}
	if err := copySelf(exe); err != nil {
		return fmt.Errorf("复制 velumd 到 %s：%w", exe, err)
	}
	args := []string{"--home", home, "--allow-sid", sid}
	s, err := m.OpenService(serviceName)
	if err != nil {
		s, err = m.CreateService(serviceName, exe, mgr.Config{DisplayName: "Velum", StartType: mgr.StartManual}, args...)
		if err != nil {
			return fmt.Errorf("创建服务：%w", err)
		}
	} else {
		cfg, err := s.Config()
		if err != nil {
			s.Close()
			return err
		}
		cfg.BinaryPathName = commandLine(exe, args...)
		cfg.StartType = mgr.StartManual
		if err := s.UpdateConfig(cfg); err != nil {
			s.Close()
			return fmt.Errorf("更新服务：%w", err)
		}
	}
	defer s.Close()
	cfg, _ := s.Config()
	cfg.Description = "Velum 网络核心：TUN 与系统代理（由 Velum App 启动与停止）"
	_ = s.UpdateConfig(cfg)
	if err := s.SetRecoveryActions([]mgr.RecoveryAction{
		{Type: mgr.ServiceRestart, Delay: time.Second},
		{Type: mgr.ServiceRestart, Delay: 5 * time.Second},
		{Type: mgr.ServiceRestart, Delay: 30 * time.Second},
	}, 24*60*60); err != nil {
		return fmt.Errorf("设置失败重启：%w", err)
	}
	// 服务以非 0 退出码报告停止（如启动失败）也按失败处理并重启，而不只是进程崩溃
	if err := s.SetRecoveryActionsOnNonCrashFailures(true); err != nil {
		return fmt.Errorf("设置失败重启：%w", err)
	}
	if err := setServiceDACL(s.Handle, sid); err != nil {
		return fmt.Errorf("设置服务权限：%w", err)
	}
	return startAndWait(s)
}

func uninstallService() error {
	m, err := mgr.Connect()
	if err != nil {
		return fmt.Errorf("连接服务管理器（需要管理员权限）：%w", err)
	}
	defer m.Disconnect()
	s, err := m.OpenService(serviceName)
	if err == nil {
		if err := stopAndWait(s); err != nil {
			s.Close()
			return err
		}
		err = s.Delete()
		s.Close()
		if err != nil {
			return fmt.Errorf("删除服务：%w", err)
		}
	}
	exe, err := installedExe()
	if err != nil {
		return err
	}
	for i := 0; i < 50; i++ { // 进程退出后文件才能删除
		if err = os.Remove(exe); err == nil || errors.Is(err, os.ErrNotExist) {
			_ = os.Remove(filepath.Dir(exe))
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("删除 %s：%w", exe, err)
}

// openService 以最小权限打开服务（安装用户经服务 DACL 获得启动 / 停止权限，不需要管理员）。
func openService(access uint32) (*mgr.Service, func(), error) {
	scm, err := windows.OpenSCManager(nil, nil, windows.SC_MANAGER_CONNECT)
	if err != nil {
		return nil, nil, err
	}
	name, _ := windows.UTF16PtrFromString(serviceName)
	h, err := windows.OpenService(scm, name, access)
	if err != nil {
		windows.CloseServiceHandle(scm)
		return nil, nil, err
	}
	s := &mgr.Service{Name: serviceName, Handle: h}
	return s, func() { s.Close(); windows.CloseServiceHandle(scm) }, nil
}

func controlService(start bool) error {
	access := uint32(windows.SERVICE_QUERY_STATUS)
	if start {
		access |= windows.SERVICE_START
	} else {
		access |= windows.SERVICE_STOP
	}
	s, closeFn, err := openService(access)
	if err != nil {
		if errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			return errors.New("Velum 服务未安装")
		}
		return err
	}
	defer closeFn()
	if start {
		return startAndWait(s)
	}
	return stopAndWait(s)
}

func startAndWait(s *mgr.Service) error {
	if err := s.Start(); err != nil && !errors.Is(err, windows.ERROR_SERVICE_ALREADY_RUNNING) {
		return fmt.Errorf("启动服务：%w", err)
	}
	return waitState(s, svc.Running, 15*time.Second)
}

func stopAndWait(s *mgr.Service) error {
	if _, err := s.Control(svc.Stop); err != nil &&
		!errors.Is(err, windows.ERROR_SERVICE_NOT_ACTIVE) && !errors.Is(err, windows.ERROR_SERVICE_CANNOT_ACCEPT_CTRL) {
		return fmt.Errorf("停止服务：%w", err)
	}
	return waitState(s, svc.Stopped, 15*time.Second)
}

func waitState(s *mgr.Service, want svc.State, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		q, err := s.Query()
		if err != nil {
			return err
		}
		if q.State == want {
			return nil
		}
		if want == svc.Running && q.State == svc.Stopped {
			return errors.New("服务启动后立即退出，查看 Velum 目录下的 logs\\velumd.log")
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("等待服务状态超时（当前 %d）", q.State)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// setServiceDACL 在系统默认服务权限的基础上，允许安装用户查询、启动、停止服务。
func setServiceDACL(h windows.Handle, sid string) error {
	sd, err := windows.SecurityDescriptorFromString(serviceSDDL(sid))
	if err != nil {
		return err
	}
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetSecurityInfo(h, windows.SE_SERVICE, windows.DACL_SECURITY_INFORMATION, nil, nil, dacl, nil)
}

// serviceSDDL：SYSTEM / Administrators / 交互用户 / 服务用户沿用系统默认；安装用户增加启动（RP）、停止（WP）。
func serviceSDDL(sid string) string {
	return "D:(A;;CCLCSWRPWPDTLOCRRC;;;SY)(A;;CCDCLCSWRPWPDTLOCRSDRCWDWO;;;BA)(A;;CCLCSWLOCRRC;;;IU)(A;;CCLCSWLOCRRC;;;SU)" +
		"(A;;CCLCSWRPWPLORC;;;" + sid + ")"
}

func commandLine(exe string, args ...string) string {
	parts := []string{syscall.EscapeArg(exe)}
	for _, a := range args {
		parts = append(parts, syscall.EscapeArg(a))
	}
	return strings.Join(parts, " ")
}

// copySelf 把当前程序复制到 dst（先写临时文件再替换，避免留下半个文件）。
func copySelf(dst string) error {
	self, err := os.Executable()
	if err != nil {
		return err
	}
	if same, _ := sameFile(self, dst); same {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	src, err := os.Open(self)
	if err != nil {
		return err
	}
	defer src.Close()
	tmp := dst + ".new"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	if _, err := io.Copy(f, src); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, dst)
}

func sameFile(a, b string) (bool, error) {
	sa, err := os.Stat(a)
	if err != nil {
		return false, err
	}
	sb, err := os.Stat(b)
	if err != nil {
		return false, err
	}
	return os.SameFile(sa, sb), nil
}

// ServiceStatus 是 velumd service status 的输出（供 App 判断是否需要安装 / 更新）。
type ServiceStatus struct {
	Installed bool   `json:"installed"`
	Running   bool   `json:"running"`
	Binary    string `json:"binary,omitempty"`
	// Outdated 表示已安装的程序与当前程序（App 内置的 velumd）内容不同，需要重新安装以更新。
	Outdated bool   `json:"outdated"`
	Error    string `json:"error,omitempty"`
}

func serviceStatus() ServiceStatus {
	var st ServiceStatus
	s, closeFn, err := openService(windows.SERVICE_QUERY_STATUS)
	if err != nil {
		if !errors.Is(err, windows.ERROR_SERVICE_DOES_NOT_EXIST) {
			st.Error = err.Error()
		}
		return st
	}
	defer closeFn()
	st.Installed = true
	if q, err := s.Query(); err == nil {
		st.Running = q.State == svc.Running
	}
	exe, err := installedExe()
	if err != nil {
		st.Error = err.Error()
		return st
	}
	st.Binary = exe
	self, _ := os.Executable()
	if same, _ := sameFile(self, exe); !same {
		a, errA := fileHash(self)
		b, errB := fileHash(exe)
		st.Outdated = errA == nil && (errB != nil || a != b)
	}
	return st
}

func fileHash(p string) ([32]byte, error) {
	f, err := os.Open(p)
	if err != nil {
		return [32]byte{}, err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return [32]byte{}, err
	}
	var out [32]byte
	copy(out[:], h.Sum(nil))
	return out, nil
}

func nonEmpty(s string) []string {
	if s == "" {
		return nil
	}
	return []string{s}
}
