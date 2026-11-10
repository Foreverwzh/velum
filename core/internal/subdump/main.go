// subdump：订阅解析对照测试的 Velum 侧。把 subscription.Parse 的结果（ir.Config，含告警）输出为 JSON；
// velum-engine 的 velum-sub 对同样的输入输出同样的 JSON，由 velum-tools sub-diff 逐项比对。
//
//	go run ./internal/subdump -batch DIR          解析 DIR 下每个样例（*.yaml / *.json），结果写到 DIR/<名字>.go.out
//	go run ./internal/subdump -live -out DIR      取本机 Velum 的当前订阅，连同读到的 provider 内容一起存成样例
//	go run ./internal/subdump -corpus DIR         从各包测试代码里抽取内联的订阅样例
//
// 样例旁边可以有同名的 .reads 目录（reads.json + 内容文件）：provider 读取按 (path, url) 回放，
// 没记录的按本地路径读（相对路径以样例所在目录为基准）。含节点凭据的样例只放在 git 忽略的目录里，权限 0600。
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/Foreverwzh/velum/core/pkg/config"
	"github.com/Foreverwzh/velum/core/pkg/source"
	"github.com/Foreverwzh/velum/core/pkg/subscription"
)

// read 是一次 provider 读取的记录。
type read struct {
	Path  string `json:"path"`
	URL   string `json:"url"`
	File  string `json:"file,omitempty"`
	Error string `json:"error,omitempty"`
}

// meta 是样例的解析参数（<名字>.meta.json，可选）。
type meta struct {
	File          string   `json:"file"`
	BaseDirs      []string `json:"base_dirs"` // 相对样例所在目录
	GeoRuleSetURL string   `json:"geo_rule_set_url"`
}

type output struct {
	Config any    `json:"config,omitempty"`
	Error  string `json:"error,omitempty"`
}

func main() {
	batch := flag.String("batch", "", "样例目录")
	live := flag.Bool("live", false, "取本机 Velum 的当前订阅")
	out := flag.String("out", "", "-live 的输出目录")
	home := flag.String("home", "", "Velum 目录（默认与 velumd 相同）")
	corpus := flag.String("corpus", "", "从测试代码抽取样例到这个目录")
	flag.Parse()
	switch {
	case *batch != "":
		fail(runBatch(*batch))
	case *live:
		fail(runLive(*out, *home))
	case *corpus != "":
		fail(runCorpus(*corpus))
	default:
		flag.Usage()
		os.Exit(2)
	}
}

func fail(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "subdump:", err)
		os.Exit(1)
	}
}

func isSample(name string) bool {
	return (strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".json")) && !strings.HasSuffix(name, ".meta.json")
}

func runBatch(dir string) error {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	n := 0
	for _, e := range ents {
		if e.IsDir() || !isSample(e.Name()) {
			continue
		}
		b, err := parseSample(filepath.Join(dir, e.Name()))
		if err != nil {
			return fmt.Errorf("%s: %w", e.Name(), err)
		}
		if err := os.WriteFile(filepath.Join(dir, e.Name()+".go.out"), b, 0o600); err != nil {
			return err
		}
		n++
	}
	fmt.Fprintf(os.Stderr, "subdump: %d 个样例\n", n)
	return nil
}

func parseSample(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	dir := filepath.Dir(path)
	var m meta
	if b, err := os.ReadFile(strings.TrimSuffix(path, filepath.Ext(path)) + ".meta.json"); err == nil {
		if err := json.Unmarshal(b, &m); err != nil {
			return nil, err
		}
	}
	reads := map[[2]string]read{}
	readsDir := path + ".reads"
	if b, err := os.ReadFile(filepath.Join(readsDir, "reads.json")); err == nil {
		var list []read
		if err := json.Unmarshal(b, &list); err != nil {
			return nil, err
		}
		for _, r := range list {
			reads[[2]string{r.Path, r.URL}] = r
		}
	}
	opts := subscription.Options{File: m.File, GeoRuleSetURL: m.GeoRuleSetURL, ReadFile: func(p, u string) ([]byte, error) {
		if r, ok := reads[[2]string{p, u}]; ok {
			if r.Error != "" {
				return nil, fmt.Errorf("%s", r.Error)
			}
			return os.ReadFile(filepath.Join(readsDir, r.File))
		}
		if p == "" {
			return nil, fmt.Errorf("no path")
		}
		if !filepath.IsAbs(p) {
			p = filepath.Join(dir, p)
		}
		return os.ReadFile(p)
	}}
	for _, d := range m.BaseDirs {
		opts.BaseDirs = append(opts.BaseDirs, filepath.Join(dir, d))
	}
	var o output
	func() {
		// 解析崩溃（Go 版的缺陷）也记成一个结果，不中断整批
		defer func() {
			if r := recover(); r != nil {
				o = output{Error: fmt.Sprintf("panic: %v", r)}
			}
		}()
		cfg, err := subscription.Parse(data, opts)
		if err != nil {
			o.Error = err.Error()
		} else {
			o.Config = cfg
		}
	}()
	b, err := json.MarshalIndent(o, "", " ")
	if err != nil {
		// 例如 Options 里有 JSON 不能表示的值（NaN、非字符串键的映射）
		b, _ = json.Marshal(output{Error: "marshal: " + err.Error()})
	}
	return b, nil
}

// runLive 把本机 Velum 的当前订阅及其 provider 读取存成样例 DIR/live.yaml（+ live.yaml.reads/）。
func runLive(out, home string) error {
	if out == "" {
		return fmt.Errorf("缺少 -out")
	}
	if home == "" {
		h, err := config.Home()
		if err != nil {
			return err
		}
		home = h
	}
	paths := config.PathsFor(home)
	cfg, err := config.Load(paths)
	if err != nil {
		return err
	}
	f := source.New(paths.Cache, cfg.Subscription.UserAgent)
	h := sha256.Sum256([]byte(cfg.Subscription.URL))
	data, err := os.ReadFile(filepath.Join(paths.Cache, "subscription", hex.EncodeToString(h[:8])))
	if err != nil {
		return err
	}
	readsDir := filepath.Join(out, "live.yaml.reads")
	if err := os.MkdirAll(readsDir, 0o700); err != nil {
		return err
	}
	var mu sync.Mutex
	var list []read
	reader := f.ProviderReader()
	rec := func(p, u string) ([]byte, error) {
		b, err := reader(p, u)
		mu.Lock()
		defer mu.Unlock()
		r := read{Path: p, URL: u}
		if err != nil {
			r.Error = err.Error()
		} else {
			r.File = fmt.Sprintf("r%03d", len(list))
			if werr := os.WriteFile(filepath.Join(readsDir, r.File), b, 0o600); werr != nil {
				return nil, werr
			}
		}
		list = append(list, r)
		return b, err
	}
	if _, err := subscription.Parse(data, subscription.Options{File: "subscription", ReadFile: rec, GeoRuleSetURL: cfg.Subscription.GeoRuleSetURL}); err != nil {
		return err
	}
	sort.Slice(list, func(i, j int) bool { return list[i].File < list[j].File })
	b, _ := json.MarshalIndent(list, "", " ")
	if err := os.WriteFile(filepath.Join(readsDir, "reads.json"), b, 0o600); err != nil {
		return err
	}
	m, _ := json.Marshal(meta{File: "subscription", GeoRuleSetURL: cfg.Subscription.GeoRuleSetURL})
	if err := os.WriteFile(filepath.Join(out, "live.meta.json"), m, 0o600); err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "subdump: 订阅 %d 字节，provider 读取 %d 次\n", len(data), len(list))
	return os.WriteFile(filepath.Join(out, "live.yaml"), data, 0o600)
}

// runCorpus 从 core 各包的测试代码里抽取像订阅的字符串字面量（Clash 的 proxies / sing-box 的 outbounds）。
func runCorpus(out string) error {
	if err := os.MkdirAll(out, 0o700); err != nil {
		return err
	}
	seen := map[string]bool{}
	n := 0
	err := filepath.WalkDir(".", func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "third_party" || d.Name() == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(token.NewFileSet(), p, nil, 0)
		if err != nil {
			return err
		}
		ast.Inspect(f, func(x ast.Node) bool {
			lit, ok := x.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			s, err := strconv.Unquote(lit.Value)
			if err != nil || len(s) < 20 {
				return true
			}
			ext := ""
			switch {
			case strings.Contains(s, "proxies:") || strings.Contains(s, "proxy-groups:") || strings.Contains(s, "rules:\n"):
				ext = ".yaml"
			case strings.Contains(s, `"outbounds"`) || strings.Contains(s, `"endpoints"`):
				ext = ".json"
			default:
				return true
			}
			h := sha256.Sum256([]byte(s))
			key := hex.EncodeToString(h[:6])
			if seen[key] {
				return true
			}
			seen[key] = true
			name := strings.ReplaceAll(strings.TrimSuffix(filepath.Base(p), "_test.go"), ".", "_") + "-" + key + ext
			if werr := os.WriteFile(filepath.Join(out, name), []byte(s), 0o600); werr != nil {
				err = werr
			}
			n++
			return true
		})
		return err
	})
	fmt.Fprintf(os.Stderr, "subdump: 抽取 %d 个样例\n", n)
	return err
}
