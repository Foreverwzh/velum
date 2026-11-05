//go:build !linux || android

package main

import "github.com/Foreverwzh/velum/core/pkg/rpc"

func setTUN(c *rpc.Client, args []string) error { return setSwitch(c, "TUN", args) }
