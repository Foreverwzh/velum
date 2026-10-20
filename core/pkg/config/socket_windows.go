package config

import (
	"path/filepath"

	"golang.org/x/sys/windows"
)

// socketPaths：本用户的 Pipe 优先，其次 Velum 服务的 Pipe。
func socketPaths(string) (string, string) {
	if u, err := windows.GetCurrentProcessToken().GetTokenUser(); err == nil {
		return UserPipe(u.User.Sid.String()), ServicePipe
	}
	return ServicePipe, ServicePipe
}

// ServiceDataDir 是 Velum 服务的数据目录 %ProgramData%\Velum。
func ServiceDataDir() (string, error) {
	pd, err := windows.KnownFolderPath(windows.FOLDERID_ProgramData, 0)
	if err != nil {
		return "", err
	}
	return filepath.Join(pd, "Velum"), nil
}
