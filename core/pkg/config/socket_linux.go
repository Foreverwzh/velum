//go:build linux && !android

package config

import "path/filepath"

const (
	// ServiceHome、ServiceDataDir、ServiceSocket 是 Linux 系统服务的配置、数据与本地接口（velumd service install）。
	ServiceHome    = "/etc/velum"
	ServiceDataDir = "/var/lib/velum"
	ServiceSocket  = "/run/velum/velumd.sock"
)

// socketPaths：客户端先试本用户的 socket，再回落到系统服务的。
func socketPaths(home string) (string, string) {
	return filepath.Join(home, "velumd.sock"), ServiceSocket
}
