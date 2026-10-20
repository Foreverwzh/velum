package config

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestSeedData(t *testing.T) {
	user, svc := PathsFor(t.TempDir()), PathsFor(t.TempDir())
	svc = svc.WithData(svc.Home)
	old := time.Now().Add(-48 * time.Hour)
	write := func(p, s string, mt time.Time) {
		_ = os.MkdirAll(filepath.Dir(p), 0o700)
		if err := os.WriteFile(p, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
		_ = os.Chtimes(p, mt, mt)
	}
	write(filepath.Join(user.Cache, "providers", "a"), "user-a", old)
	write(filepath.Join(user.Cache, "rulesets", "b"), "user-b", old)
	write(filepath.Join(user.Cache, "singbox.db"), "db", old) // 不复制内核自己的缓存
	write(user.State, `{"selections":{"Proxy":"HK"}}`, old)
	// 服务已有更新的同名文件：保留
	write(filepath.Join(svc.Cache, "rulesets", "b"), "svc-b", time.Now())
	if err := os.Symlink(filepath.Join(user.Cache, "providers", "a"), filepath.Join(user.Cache, "providers", "link")); err != nil {
		t.Fatal(err)
	}

	if n := SeedData(user, svc); n != 2 {
		t.Fatalf("应复制 provider a 与 state.json，实际 %d 个", n)
	}
	read := func(p string) string { b, _ := os.ReadFile(p); return string(b) }
	if read(filepath.Join(svc.Cache, "providers", "a")) != "user-a" || read(filepath.Join(svc.Cache, "rulesets", "b")) != "svc-b" {
		t.Fatal("缓存复制结果不对")
	}
	if fi, err := os.Stat(filepath.Join(svc.Cache, "providers", "a")); err != nil || fi.ModTime().Sub(old).Abs() > time.Second {
		t.Fatalf("应保留修改时间（据此判断缓存是否过期）：%v", fi.ModTime())
	}
	if _, err := os.Lstat(filepath.Join(svc.Cache, "providers", "link")); err == nil {
		t.Fatal("不应复制符号链接")
	}
	if _, err := os.Stat(filepath.Join(svc.Cache, "singbox.db")); err == nil {
		t.Fatal("不应复制内核缓存")
	}
	if read(svc.State) == "" {
		t.Fatal("应复制策略组选择")
	}
	if n := SeedData(user, svc); n != 0 {
		t.Fatalf("再次运行不应重复复制，实际 %d 个", n)
	}
}
