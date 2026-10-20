// Package winuser 供以服务（SYSTEM）运行的 velumd 代表已登录用户行事：取得用户令牌、以用户身份访问文件、
// 在用户会话中启动子进程（WinINet 等用户会话设置）。
package winuser

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"slices"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"golang.org/x/sys/windows"
)

// ErrNoUser 表示没有已登录的、允许的用户。
var ErrNoUser = errors.New("没有已登录的 Velum 用户")

// Token 找到要代为操作的用户会话的令牌：优先活动控制台会话，其次其他活动会话（如远程桌面），且用户须在 allow 中
// （allow 为空时只接受控制台会话的用户）。调用方负责关闭。
func Token(allow []string) (windows.Token, error) {
	try := func(id uint32) (windows.Token, bool) {
		var t windows.Token
		if windows.WTSQueryUserToken(id, &t) != nil {
			return 0, false
		}
		u, err := t.GetTokenUser()
		if err == nil && (len(allow) == 0 || slices.Contains(allow, u.User.Sid.String())) {
			return t, true
		}
		t.Close()
		return 0, false
	}
	if t, ok := try(windows.WTSGetActiveConsoleSessionId()); ok {
		return t, nil
	}
	if len(allow) > 0 {
		var sessions *windows.WTS_SESSION_INFO
		var n uint32
		if err := windows.WTSEnumerateSessions(0, 0, 1, &sessions, &n); err == nil {
			defer windows.WTSFreeMemory(uintptr(unsafe.Pointer(sessions)))
			for _, s := range unsafe.Slice(sessions, n) {
				if s.State != windows.WTSActive {
					continue
				}
				if t, ok := try(s.SessionID); ok {
					return t, nil
				}
			}
		}
	}
	return 0, ErrNoUser
}

// Impersonate 以用户身份执行 fn（在独占的系统线程上模拟用户令牌）。fn 中打开的文件按用户权限检查，
// 用户借目录联接等手段重定向也只能访问自己本来就能访问的文件。
func Impersonate(allow []string, fn func() error) error {
	tok, err := Token(allow)
	if err != nil {
		return err
	}
	defer tok.Close()
	var imp windows.Token
	if err := windows.DuplicateTokenEx(tok, windows.TOKEN_IMPERSONATE|windows.TOKEN_QUERY, nil,
		windows.SecurityImpersonation, windows.TokenImpersonation, &imp); err != nil {
		return fmt.Errorf("复制用户令牌：%w", err)
	}
	defer imp.Close()
	done := make(chan error, 1)
	go func() {
		runtime.LockOSThread()
		if err := windows.SetThreadToken(nil, imp); err != nil {
			runtime.UnlockOSThread()
			done <- fmt.Errorf("模拟用户：%w", err)
			return
		}
		err := fn()
		if windows.RevertToSelf() == nil {
			runtime.UnlockOSThread()
		} // 还原失败时不解锁：goroutine 结束后运行时销毁该线程，不会以用户身份复用
		done <- err
	}()
	return <-done
}

// Run 在用户会话中以用户身份运行 exe（不显示窗口），stdin 写入后关闭，返回 stdout。
// 必须指定桌面 winsta0\default：否则子进程继承服务的窗口站，用户令牌无权访问，加载 user32 / wininet 时初始化失败。
func Run(allow []string, exe string, args []string, stdin []byte, timeout time.Duration) ([]byte, error) {
	tok, err := Token(allow)
	if err != nil {
		return nil, err
	}
	defer tok.Close()
	var env *uint16
	if err := windows.CreateEnvironmentBlock(&env, tok, false); err != nil {
		return nil, fmt.Errorf("读取用户环境：%w", err)
	}
	defer windows.DestroyEnvironmentBlock(env)

	// 可继承的管道句柄在创建子进程前后持有 ForkLock，避免被其他并发创建的子进程继承（与 os/exec 一致）
	syscall.ForkLock.Lock()
	inR, inW, err := pipe(true)
	if err != nil {
		syscall.ForkLock.Unlock()
		return nil, err
	}
	outR, outW, err := pipe(false)
	if err != nil {
		syscall.ForkLock.Unlock()
		inR.Close()
		inW.Close()
		return nil, err
	}
	errR, errW, err := pipe(false)
	if err != nil {
		syscall.ForkLock.Unlock()
		inR.Close()
		inW.Close()
		outR.Close()
		outW.Close()
		return nil, err
	}
	desktop, _ := windows.UTF16PtrFromString(`winsta0\default`)
	si := windows.StartupInfo{
		Cb: uint32(unsafe.Sizeof(windows.StartupInfo{})), Desktop: desktop,
		Flags:      windows.STARTF_USESTDHANDLES | windows.STARTF_USESHOWWINDOW,
		ShowWindow: windows.SW_HIDE,
		StdInput:   windows.Handle(inR.Fd()), StdOutput: windows.Handle(outW.Fd()), StdErr: windows.Handle(errW.Fd()),
	}
	cmdLine := make([]string, 0, len(args)+1)
	cmdLine = append(cmdLine, syscall.EscapeArg(exe))
	for _, a := range args {
		cmdLine = append(cmdLine, syscall.EscapeArg(a))
	}
	app, _ := windows.UTF16PtrFromString(exe)
	cl, _ := windows.UTF16PtrFromString(strings.Join(cmdLine, " "))
	var pi windows.ProcessInformation
	err = windows.CreateProcessAsUser(tok, app, cl, nil, nil, true,
		windows.CREATE_UNICODE_ENVIRONMENT|windows.CREATE_NO_WINDOW, env, nil, &si, &pi)
	// 子进程一端在父进程中关闭（子进程已继承）
	inR.Close()
	outW.Close()
	errW.Close()
	syscall.ForkLock.Unlock()
	if err != nil {
		inW.Close()
		outR.Close()
		errR.Close()
		return nil, fmt.Errorf("在用户会话中启动 %s：%w", exe, err)
	}
	defer windows.CloseHandle(pi.Process)
	windows.CloseHandle(pi.Thread)

	go func() {
		_, _ = inW.Write(stdin)
		inW.Close()
	}()
	var stdout, stderr bytes.Buffer
	copied := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(&stdout, outR); outR.Close(); copied <- struct{}{} }()
	go func() { _, _ = io.Copy(&stderr, errR); errR.Close(); copied <- struct{}{} }()

	if ev, _ := windows.WaitForSingleObject(pi.Process, uint32(timeout/time.Millisecond)); ev != windows.WAIT_OBJECT_0 {
		_ = windows.TerminateProcess(pi.Process, 1)
		<-copied
		<-copied
		return nil, fmt.Errorf("%s 超时未结束", exe)
	}
	<-copied
	<-copied
	var code uint32
	_ = windows.GetExitCodeProcess(pi.Process, &code)
	if code != 0 {
		return nil, fmt.Errorf("%s 失败（退出码 %d）：%s", exe, code, strings.TrimSpace(stderr.String()))
	}
	return stdout.Bytes(), nil
}

// pipe 创建匿名管道，只有交给子进程的一端可继承（childReads 为 true 时是读端）。
func pipe(childReads bool) (r, w *os.File, err error) {
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), InheritHandle: 1}
	var rh, wh windows.Handle
	if err := windows.CreatePipe(&rh, &wh, &sa, 0); err != nil {
		return nil, nil, err
	}
	parent := wh
	if !childReads {
		parent = rh
	}
	if err := windows.SetHandleInformation(parent, windows.HANDLE_FLAG_INHERIT, 0); err != nil {
		windows.CloseHandle(rh)
		windows.CloseHandle(wh)
		return nil, nil, err
	}
	return os.NewFile(uintptr(rh), "pipe-r"), os.NewFile(uintptr(wh), "pipe-w"), nil
}
