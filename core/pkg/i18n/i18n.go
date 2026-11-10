// Package i18n 翻译面向用户的文字。源文字就是中文，同时作为查找英文的键：代码读起来和以前一样，
// 没有翻译时显示中文（catalog_test.go 保证不会发生）。日志不翻译：它们是给开发者看的。
//
// 语言是进程级的：一个核心只服务一个用户。没有调用 SetLocale 时是中文。
package i18n

import (
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync/atomic"
)

// 支持的语言。
const (
	Zh = "zh"
	En = "en"
)

var english atomic.Bool

// SetLocale 设置语言。接受 BCP 47 或 POSIX 写法（zh-Hans-CN、zh_CN.UTF-8、en）；中文以外都用英文。
func SetLocale(l string) { english.Store(Normalize(l) == En) }

// Locale 返回当前语言。
func Locale() string {
	if english.Load() {
		return En
	}
	return Zh
}

// Normalize 把语言标记归一为 Zh 或 En。
func Normalize(l string) string {
	if strings.HasPrefix(strings.ToLower(strings.TrimSpace(l)), "zh") {
		return Zh
	}
	return En
}

// Detect 从环境判断语言：VELUM_LANG，其次平台的系统语言，其次 LC_ALL / LC_MESSAGES / LANG。
// 都没有（如 systemd 服务）时用英文。
func Detect() string {
	if v := os.Getenv("VELUM_LANG"); v != "" {
		return Normalize(v)
	}
	if v := systemLanguage(); v != "" {
		return Normalize(v)
	}
	for _, k := range []string{"LC_ALL", "LC_MESSAGES", "LANG"} {
		if v := os.Getenv(k); v != "" && v != "C" && v != "POSIX" && !strings.HasPrefix(v, "C.") {
			return Normalize(v)
		}
	}
	return En
}

// English 返回英文表（中文源文字 → 英文）。iOS 的 Rust 核心按同样的键查找：编译 App 时导出成 JSON 交给它。
func English() map[string]string { return en }

func lookup(src string) string {
	if english.Load() {
		if s, ok := en[src]; ok {
			return s
		}
	}
	return src
}

// T 翻译 src；有参数时按 fmt 格式化。
func T(src string, args ...any) string {
	s := lookup(src)
	if len(args) == 0 {
		return s
	}
	return fmt.Sprintf(s, args...)
}

// Static 用于包级错误变量：包初始化时语言还没设置，要到读取时才翻译。可以用 errors.Is 比较。
func Static(src string) error { return staticErr(src) }

type staticErr string

func (e staticErr) Error() string { return lookup(string(e)) }

// New 相当于 errors.New(T(src))。
func New(src string) error { return errors.New(lookup(src)) }

// Errorf 相当于 fmt.Errorf，格式串先翻译，支持 %w。
func Errorf(src string, args ...any) error { return fmt.Errorf(lookup(src), args...) }

// Printf 相当于 fmt.Printf，格式串先翻译。
func Printf(src string, args ...any) (int, error) { return fmt.Printf(lookup(src), args...) }

// Fprintf 相当于 fmt.Fprintf，格式串先翻译。
func Fprintf(w io.Writer, src string, args ...any) (int, error) {
	return fmt.Fprintf(w, lookup(src), args...)
}
