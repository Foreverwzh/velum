package sysproxy

import (
	"fmt"
	"runtime"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	wininet              = windows.NewLazySystemDLL("wininet.dll")
	procInternetQueryOpt = wininet.NewProc("InternetQueryOptionW")
	procInternetSetOpt   = wininet.NewProc("InternetSetOptionW")
	procGlobalFree       = windows.NewLazySystemDLL("kernel32.dll").NewProc("GlobalFree")
)

const (
	internetOptionRefresh              = 37
	internetOptionSettingsChanged      = 39
	internetOptionPerConnectionOption  = 75
	internetOptionProxySettingsChanged = 95

	perConnFlags         = 1
	perConnProxyServer   = 2
	perConnProxyBypass   = 3
	perConnAutoConfigURL = 4
	perConnFlagsUI       = 10 // 与界面一致的标志（Windows 7 起）；查询失败时回落 perConnFlags
)

// perConnOption 对应 INTERNET_PER_CONN_OPTIONW：Value 是 DWORD / LPWSTR / FILETIME 的联合体，
// 用 uint64 保证与 C 布局一致（64 位偏移 8、32 位偏移 4）。
type perConnOption struct {
	Option uint32
	Value  uint64
}

func (o *perConnOption) str() **uint16 { return (**uint16)(unsafe.Pointer(&o.Value)) }

// perConnList 对应 INTERNET_PER_CONN_OPTION_LISTW。Connection 为空表示默认连接（局域网）。
type perConnList struct {
	Size        uint32
	Connection  *uint16
	OptionCount uint32
	OptionError uint32
	Options     *perConnOption
}

// WinINetBackend 在当前进程（当前用户）中读写 WinINet 设置。
type WinINetBackend struct{}

func (WinINetBackend) Read() (WinSettings, error) {
	var s WinSettings
	opts := []perConnOption{{Option: perConnFlagsUI}, {Option: perConnProxyServer}, {Option: perConnProxyBypass}, {Option: perConnAutoConfigURL}}
	if err := queryPerConn(opts); err != nil {
		opts[0] = perConnOption{Option: perConnFlags}
		if err := queryPerConn(opts); err != nil {
			return s, fmt.Errorf("InternetQueryOption：%w", err)
		}
	}
	s.Flags = uint32(opts[0].Value)
	for i, dst := range []*string{&s.Server, &s.Bypass, &s.AutoConfigURL} {
		if p := *opts[i+1].str(); p != nil {
			*dst = windows.UTF16PtrToString(p)
			_, _, _ = procGlobalFree.Call(uintptr(unsafe.Pointer(p)))
		}
	}
	return s, nil
}

func queryPerConn(opts []perConnOption) error {
	list := perConnList{OptionCount: uint32(len(opts)), Options: &opts[0]}
	list.Size = uint32(unsafe.Sizeof(list))
	size := list.Size
	r, _, err := procInternetQueryOpt.Call(0, internetOptionPerConnectionOption, uintptr(unsafe.Pointer(&list)), uintptr(unsafe.Pointer(&size)))
	runtime.KeepAlive(opts)
	if r == 0 {
		return err
	}
	return nil
}

func (WinINetBackend) Write(s WinSettings) error {
	opts := []perConnOption{{Option: perConnFlags, Value: uint64(s.Flags)}, {Option: perConnProxyServer}, {Option: perConnProxyBypass}, {Option: perConnAutoConfigURL}}
	for i, v := range []string{s.Server, s.Bypass, s.AutoConfigURL} {
		p, err := windows.UTF16PtrFromString(v)
		if err != nil {
			return err
		}
		*opts[i+1].str() = p
	}
	list := perConnList{OptionCount: uint32(len(opts)), Options: &opts[0]}
	list.Size = uint32(unsafe.Sizeof(list))
	r, _, err := procInternetSetOpt.Call(0, internetOptionPerConnectionOption, uintptr(unsafe.Pointer(&list)), uintptr(list.Size))
	runtime.KeepAlive(opts)
	if r == 0 {
		return fmt.Errorf("InternetSetOption：%w", err)
	}
	// 通知已运行的应用重新读取设置
	for _, o := range []uintptr{internetOptionProxySettingsChanged, internetOptionSettingsChanged, internetOptionRefresh} {
		_, _, _ = procInternetSetOpt.Call(0, o, 0, 0)
	}
	return nil
}
