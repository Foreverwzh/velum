//go:build linux && !android

package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"os"
	"path/filepath"
	"testing"
)

func tarball(t *testing.T, path string, files map[string]string) {
	t.Helper()
	var b bytes.Buffer
	gz := gzip.NewWriter(&b)
	tw := tar.NewWriter(gz)
	for n, c := range files {
		if err := tw.WriteHeader(&tar.Header{Name: n, Mode: 0o755, Size: int64(len(c)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(c)); err != nil {
			t.Fatal(err)
		}
	}
	tw.Close()
	gz.Close()
	if err := os.WriteFile(path, b.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestUnpackBinaries(t *testing.T) {
	dir := t.TempDir()
	pkg := filepath.Join(dir, "p.tar.gz")
	// 路径穿越的条目只按文件名取，不会写到目标目录之外
	tarball(t, pkg, map[string]string{"Velum-1/velumd": "d", "Velum-1/vlu": "u", "../../evil": "x", "Velum-1/LICENSE": "l"})
	out := filepath.Join(dir, "out")
	if err := unpackBinaries(pkg, out); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(out)
	if len(entries) != 2 {
		t.Fatalf("应只取出两个程序：%v", entries)
	}
	if _, err := os.Stat(filepath.Join(dir, "evil")); err == nil {
		t.Fatal("路径穿越")
	}
	bad := filepath.Join(dir, "bad.tar.gz")
	tarball(t, bad, map[string]string{"Velum-1/velumd": "d"})
	if err := unpackBinaries(bad, filepath.Join(dir, "out2")); err == nil {
		t.Fatal("缺少 vlu 应报错")
	}
}

func TestPkgName(t *testing.T) {
	m := pkgName.FindStringSubmatch("Velum-0.2.0-rc.1-linux-arm64.tar.gz")
	if m == nil || m[1] != "0.2.0-rc.1" || m[2] != "arm64" {
		t.Fatalf("%v", m)
	}
	if pkgName.MatchString("Velum-0.2.0-macos-arm64.zip") {
		t.Fatal("不是 Linux 压缩包")
	}
}
