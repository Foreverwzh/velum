//go:build !linux || android

package main

import (
	"errors"

	"github.com/Foreverwzh/velum/core/pkg/rpc"
)

const upgradeHint = "在 App 中更新"

func upgrade(*rpc.Client, []string) error {
	return errors.New("vlu upgrade 只用于 Linux 服务器；其他平台在 App 中更新")
}
