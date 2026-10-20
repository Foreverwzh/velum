package logfile_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Foreverwzh/velum/core/pkg/logfile"
)

func TestRotate(t *testing.T) {
	p := filepath.Join(t.TempDir(), "v.log")
	w, err := logfile.Open(p, 100, 2)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.Repeat("x", 39) + "\n" // 40B
	for i := 0; i < 10; i++ {
		_, _ = w.Write([]byte(line))
	}
	_ = w.Close()
	for _, f := range []string{p, p + ".1", p + ".2"} {
		st, err := os.Stat(f)
		if err != nil || st.Size() > 100 {
			t.Fatalf("%s: %v %v", f, err, st)
		}
	}
	if _, err := os.Stat(p + ".3"); err == nil {
		t.Fatal("只应保留 2 份旧文件")
	}
}
