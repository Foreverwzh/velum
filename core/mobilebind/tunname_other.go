//go:build !darwin

package mobilebind

func tunName(int) string { return "tun" }
