package singbox

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/sagernet/bbolt"
)

// 启动前只保留 fake-ip 的数据：策略组选择等以 Velum 的 state.json 为准，不能被上次的缓存覆盖。
func TestKeepOnlyFakeIP(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cache.db")
	db, err := bbolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Update(func(tx *bbolt.Tx) error {
		for _, b := range []string{"selected", "group_expand", "rule_set", "\x00profile", "fakeip_address", "fakeip_domain4"} {
			bk, _ := tx.CreateBucket([]byte(b))
			_ = bk.Put([]byte("k"), []byte("v"))
		}
		return nil
	})
	db.Close()
	if err := keepOnlyFakeIP(path); err != nil {
		t.Fatal(err)
	}
	db, err = bbolt.Open(path, 0o600, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var left []string
	_ = db.View(func(tx *bbolt.Tx) error {
		return tx.ForEach(func(name []byte, _ *bbolt.Bucket) error { left = append(left, string(name)); return nil })
	})
	if len(left) != 2 || left[0] != "fakeip_address" || left[1] != "fakeip_domain4" {
		t.Fatalf("只应保留 fake-ip 的数据：%q", left)
	}
	// 文件不存在：什么也不做；损坏：返回错误（调用方删除整个文件）
	if err := keepOnlyFakeIP(filepath.Join(t.TempDir(), "none.db")); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(t.TempDir(), "bad.db")
	_ = os.WriteFile(bad, []byte("not a database"), 0o600)
	if err := keepOnlyFakeIP(bad); err == nil {
		t.Fatal("损坏的文件应返回错误")
	}
}
