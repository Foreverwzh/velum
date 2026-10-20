package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"unsafe"

	"golang.org/x/sys/windows"
)

// secureDataDir 确保服务的数据目录（%ProgramData%\Velum）只有 SYSTEM / Administrators 可写，readers（安装用户）可读。
//
// %ProgramData% 默认允许普通用户新建目录：若该目录或其中任何一项是重定向点（目录联接 / 符号链接），
// 或所有者不是 SYSTEM / Administrators（可能被他人预先创建并布置），就整个删除后重建，再设置受保护的 DACL。
func secureDataDir(dir string, readers []string) error {
	if fi, err := os.Lstat(dir); err == nil {
		if untrusted(dir, fi) || walkUntrusted(dir) {
			if err := os.RemoveAll(dir); err != nil {
				return fmt.Errorf("清理不可信的数据目录 %s：%w", dir, err)
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	sddl := "D:P(A;OICI;FA;;;SY)(A;OICI;FA;;;BA)"
	for _, s := range readers {
		if _, err := windows.StringToSid(s); err != nil {
			return fmt.Errorf("无效的 SID %q：%w", s, err)
		}
		sddl += "(A;OICI;FR;;;" + s + ")"
	}
	sd, err := windows.SecurityDescriptorFromString(sddl)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return err
	}
	p, _ := windows.UTF16PtrFromString(dir)
	sa := windows.SecurityAttributes{Length: uint32(unsafe.Sizeof(windows.SecurityAttributes{})), SecurityDescriptor: sd}
	if err := windows.CreateDirectory(p, &sa); err != nil && !errors.Is(err, windows.ERROR_ALREADY_EXISTS) {
		return fmt.Errorf("创建数据目录 %s：%w", dir, err)
	}
	// 已存在的目录：重置所有者与受保护的 DACL（继承到现有子项）。
	// 所有者设为 Administrators 组：管理员令牌只能把所有者设为自己或 Administrators，
	// 设为 SYSTEM 需要 SeRestorePrivilege，否则报 ERROR_INVALID_OWNER（1307）。
	dacl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	admins, err := windows.CreateWellKnownSid(windows.WinBuiltinAdministratorsSid)
	if err != nil {
		return err
	}
	if err := windows.SetNamedSecurityInfo(dir, windows.SE_FILE_OBJECT,
		windows.OWNER_SECURITY_INFORMATION|windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, admins, nil, dacl, nil); err != nil {
		return fmt.Errorf("设置数据目录 %s 的权限：%w", dir, err)
	}
	return nil
}

func untrusted(path string, fi fs.FileInfo) bool {
	if fi.Mode()&(fs.ModeSymlink|fs.ModeIrregular) != 0 {
		return true // 重定向点（Go 把目录联接报告为 ModeIrregular / ModeSymlink）
	}
	sd, err := windows.GetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.OWNER_SECURITY_INFORMATION)
	if err != nil {
		return true
	}
	owner, _, err := sd.Owner()
	return err != nil || !(owner.IsWellKnown(windows.WinLocalSystemSid) || owner.IsWellKnown(windows.WinBuiltinAdministratorsSid))
}

func walkUntrusted(dir string) (bad bool) {
	_ = filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			bad = true
			return filepath.SkipAll
		}
		fi, err := d.Info()
		if err != nil || untrusted(p, fi) {
			bad = true
			return filepath.SkipAll
		}
		return nil
	})
	return bad
}
