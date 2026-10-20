package config

import (
	"os"
	"path/filepath"
	"time"

	"github.com/Foreverwzh/velum/core/pkg/userfs"
)

// seedMaxFile 是复制单个缓存文件的上限（与拉取上限一致）。
const seedMaxFile = 64 << 20

// SeedData 把用户目录中 Velum 维护的数据（订阅 / provider / 规则集缓存、策略组选择）复制到 to 中缺少或更旧的位置。
//
// 用于 Windows 服务首次接手：服务的数据目录在 %ProgramData%\Velum，一开始是空的；没有缓存就只能先同步拉取全部
// provider 才能启动内核，而此时以用户身份运行的 velumd 已经停止，没有代理可用。源文件经 userfs 以用户身份读取，
// 只复制普通文件，不会借服务读到用户本来读不到的内容。内容按订阅数据对待（与用户可以随意设置订阅地址同等可信）。
func SeedData(from, to Paths) (copied int) {
	if from.Data == to.Data {
		return 0
	}
	for _, sub := range []string{"subscription", "providers", "rulesets"} {
		infos, err := userfs.ReadDir(filepath.Join(from.Cache, sub))
		if err != nil {
			continue
		}
		for _, fi := range infos {
			if seedFile(filepath.Join(from.Cache, sub, fi.Name()), filepath.Join(to.Cache, sub, fi.Name()), fi) {
				copied++
			}
		}
	}
	if fi, err := userfs.Stat(from.State); err == nil {
		if _, err := os.Stat(to.State); os.IsNotExist(err) && seedFile(from.State, to.State, fi) {
			copied++
		}
	}
	return copied
}

func seedFile(src, dst string, fi os.FileInfo) bool {
	if !fi.Mode().IsRegular() || fi.Size() > seedMaxFile || filepath.Base(src) != fi.Name() {
		return false
	}
	if cur, err := os.Stat(dst); err == nil && !cur.ModTime().Before(fi.ModTime()) {
		return false
	}
	data, err := userfs.ReadFile(src)
	if err != nil || len(data) > seedMaxFile {
		return false
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		return false
	}
	tmp := dst + ".seed"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return false
	}
	// 保留修改时间：缓存是否过期按它判断（过期的照样先用于启动，随后在后台更新）
	_ = os.Chtimes(tmp, time.Now(), fi.ModTime())
	if err := os.Rename(tmp, dst); err != nil {
		_ = os.Remove(tmp)
		return false
	}
	return true
}
