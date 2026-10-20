//go:build !darwin && !windows

package netsense

func bindToInterface(uintptr, string, int) error { return nil }
