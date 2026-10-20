// Package userfs 访问用户拥有的文件：config.yaml、overlay.yaml、订阅文件、本地 provider / 规则集。
//
// velumd 以用户身份运行时就是 os 的同名函数。Windows 服务（SYSTEM）设置 Impersonate，以已登录用户的身份执行：
// 用户借目录联接、配置中的任意路径等手段，也只能访问自己本来就能访问的文件，不会借服务提权。
// Velum 自己维护的数据（日志、缓存、状态、快照）不经此包，在服务模式下位于只有 SYSTEM / 管理员可写的目录。
package userfs

import (
	"errors"
	"io/fs"
	"os"
)

// Impersonate 非空时以用户身份执行 fn。
var Impersonate func(fn func() error) error

func do(fn func() error) error {
	if Impersonate == nil {
		return fn()
	}
	return Impersonate(fn)
}

func ReadFile(path string) (data []byte, err error) {
	err = do(func() (e error) { data, e = os.ReadFile(path); return })
	return
}

func Stat(path string) (info fs.FileInfo, err error) {
	err = do(func() (e error) { info, e = os.Stat(path); return })
	return
}

func MkdirAll(path string, perm fs.FileMode) error {
	return do(func() error { return os.MkdirAll(path, perm) })
}

// WriteFile 写入文件；不存在时创建。
func WriteFile(path string, data []byte, perm fs.FileMode) error {
	return do(func() error { return os.WriteFile(path, data, perm) })
}

// WriteFileAtomic 先写临时文件再改名替换，避免留下半个文件。
func WriteFileAtomic(path string, data []byte, perm fs.FileMode) error {
	return do(func() error {
		tmp := path + ".tmp"
		if err := os.WriteFile(tmp, data, perm); err != nil {
			return err
		}
		return os.Rename(tmp, path)
	})
}

// ReadDir 列出目录中的条目信息（不跟随符号链接；不存在时返回空列表）。
func ReadDir(path string) (infos []fs.FileInfo, err error) {
	err = do(func() error {
		entries, e := os.ReadDir(path)
		if errors.Is(e, fs.ErrNotExist) {
			return nil
		}
		if e != nil {
			return e
		}
		for _, en := range entries {
			if fi, e := en.Info(); e == nil {
				infos = append(infos, fi)
			}
		}
		return nil
	})
	return
}
