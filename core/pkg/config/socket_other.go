//go:build !windows

package config

import "path/filepath"

func socketPaths(home string) (string, string) { return filepath.Join(home, "velumd.sock"), "" }
