// Package archtest 用测试守护架构约束（ARCHITECTURE §2.2）。
package archtest

import (
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// 只有 pkg/engine/singbox/ 允许 import sing-box 及其底层库；其余代码只能通过 Engine / Renderer 接口使用内核。
var restricted = []string{"github.com/sagernet/sing-box", "github.com/sagernet/sing-tun", "github.com/sagernet/sing/"}

const allowedDir = "pkg/engine/singbox/"

func TestKernelImportBoundary(t *testing.T) {
	root, _ := filepath.Abs("../..")
	fset := token.NewFileSet()
	checked := 0
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if name := d.Name(); name == "testdata" || strings.HasPrefix(name, ".") || name == "bin" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		f, err := parser.ParseFile(fset, path, nil, parser.ImportsOnly)
		if err != nil {
			return err
		}
		checked++
		for _, imp := range f.Imports {
			p, _ := strconv.Unquote(imp.Path.Value)
			for _, r := range restricted {
				if strings.HasPrefix(p, r) && !strings.HasPrefix(rel, allowedDir) {
					t.Errorf("%s imports %s：内核依赖只允许出现在 %s", rel, p, allowedDir)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if checked < 5 {
		t.Fatalf("只检查了 %d 个文件，路径可能不对", checked)
	}
	if _, err := os.Stat(filepath.Join(root, "go.mod")); err != nil {
		t.Fatal("root 不是 core 模块根目录")
	}
}
