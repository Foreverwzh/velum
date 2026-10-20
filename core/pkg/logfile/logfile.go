// Package logfile 提供按大小轮转的日志文件。
package logfile

import (
	"fmt"
	"os"
	"sync"
)

// Writer 在文件超过 MaxSize 时轮转为 path.1、path.2 …，最多保留 Keep 份旧文件。
type Writer struct {
	Path    string
	MaxSize int64
	Keep    int

	mu   sync.Mutex
	f    *os.File
	size int64
}

func Open(path string, maxSize int64, keep int) (*Writer, error) {
	w := &Writer{Path: path, MaxSize: maxSize, Keep: keep}
	return w, w.open()
}

func (w *Writer) open() error {
	f, err := os.OpenFile(w.Path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return err
	}
	w.f, w.size = f, st.Size()
	return nil
}

func (w *Writer) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.MaxSize > 0 && w.size+int64(len(p)) > w.MaxSize && w.size > 0 {
		if err := w.rotate(); err != nil {
			return 0, err
		}
	}
	n, err := w.f.Write(p)
	w.size += int64(n)
	return n, err
}

func (w *Writer) rotate() error {
	_ = w.f.Close()
	for i := w.Keep - 1; i >= 1; i-- {
		_ = os.Rename(fmt.Sprintf("%s.%d", w.Path, i), fmt.Sprintf("%s.%d", w.Path, i+1))
	}
	if w.Keep > 0 {
		_ = os.Rename(w.Path, w.Path+".1")
	} else {
		_ = os.Remove(w.Path)
	}
	return w.open()
}

func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.f.Close()
}
