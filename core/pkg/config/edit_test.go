package config_test

import (
	"os"
	"strings"
	"testing"

	"github.com/Foreverwzh/velum/core/pkg/config"
	"gopkg.in/yaml.v3"
)

func TestSetFieldsKeepsComments(t *testing.T) {
	out, err := config.SetFields([]byte(config.Template), "subscription",
		map[string]string{"url": "https://sub.example.com/x?token=abc", "file": ""}, []string{"url", "file"})
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	for _, want := range []string{"# 订阅地址（Clash / Stash / mihomo YAML）", "# 系统代理：", "url: https://sub.example.com/x?token=abc", `file: ""`, "user_agent: clash.meta"} {
		if !strings.Contains(s, want) {
			t.Errorf("缺少 %q\n%s", want, s)
		}
	}
	// 除被修改的行外逐字节保留
	if a, b := strings.Split(config.Template, "\n"), strings.Split(s, "\n"); len(a) != len(b) {
		t.Fatalf("行数变化：%d → %d", len(a), len(b))
	} else {
		diff := 0
		for i := range a {
			if a[i] != b[i] {
				diff++
			}
		}
		if diff != 1 { // file 本来就是 ""，只有 url 一行变化
			t.Fatalf("应只改 1 行，实际 %d 行", diff)
		}
	}
	// 结果仍是合法配置，且字段生效
	dir := t.TempDir()
	p := config.PathsFor(dir)
	_ = os.WriteFile(p.Config, out, 0o600)
	cfg, err := config.Load(p)
	if err != nil || cfg.Subscription.URL != "https://sub.example.com/x?token=abc" || cfg.Subscription.File != "" {
		t.Fatalf("%v %+v", err, cfg)
	}
}

func TestSetFieldsAppends(t *testing.T) {
	out, err := config.SetFields([]byte("listen: 127.0.0.1:7899 # 端口\n"), "subscription", map[string]string{"url": "u"}, []string{"url"})
	if err != nil {
		t.Fatal(err)
	}
	if s := string(out); s != "listen: 127.0.0.1:7899 # 端口\nsubscription:\n  url: u\n" {
		t.Fatalf("%q", s)
	}
	// 节存在但缺字段：插入到节开头，沿用子项缩进；行尾注释保留
	in := "subscription: # 订阅\n    user_agent: x   # UA\nlisten: a\n"
	out, err = config.SetFields([]byte(in), "subscription", map[string]string{"url": "a b: c", "user_agent": "ua2"}, []string{"url", "user_agent"})
	if want := "subscription: # 订阅\n    url: 'a b: c'\n    user_agent: ua2 # UA\nlisten: a\n"; err != nil || string(out) != want {
		t.Fatalf("%v\n%q\nwant %q", err, out, want)
	}
	// 空节
	out, err = config.SetFields([]byte("subscription: {} # 空\n"), "subscription", map[string]string{"url": "u"}, []string{"url"})
	if want := "subscription: # 空\n  url: u\n"; err != nil || string(out) != want {
		t.Fatalf("%v %q", err, out)
	}
	if out, err = config.SetFields(nil, "subscription", map[string]string{"url": "u"}, []string{"url"}); err != nil || !strings.Contains(string(out), "url: u") {
		t.Fatalf("%v %s", err, out)
	}
}

func TestSetLists(t *testing.T) {
	apply := func(in string) string {
		t.Helper()
		out, err := config.SetLists([]byte(in), "tun", map[string][]string{"exclude_apps": {"com.bank.app", "a b"}}, []string{"exclude_apps"})
		if err != nil {
			t.Fatal(err)
		}
		return string(out)
	}
	const want = `exclude_apps: [com.bank.app, a b]`
	// 模板（单行 []，带上方注释）：只改这一行
	s := apply(config.Template)
	if !strings.Contains(s, "  "+want+"\n") || !strings.Contains(s, "# Android：不经 TUN 的应用") {
		t.Fatalf("模板：\n%s", s)
	}
	if a, b := strings.Count(config.Template, "\n"), strings.Count(s, "\n"); a != b {
		t.Fatalf("行数变化：%d → %d", a, b)
	}
	// 多行写法改为单行，其余行保留
	s = apply("tun:\n  enabled: true\n  exclude_apps: # 注释\n    - x\n    - y\n  mtu: 9000\n")
	if s != "tun:\n  enabled: true\n  "+want+" # 注释\n  mtu: 9000\n" {
		t.Fatalf("多行：\n%q", s)
	}
	// 字段不存在 / 为空
	if s = apply("tun:\n  enabled: true\n"); !strings.Contains(s, "  "+want+"\n") {
		t.Fatalf("插入：\n%s", s)
	}
	if s = apply("tun:\n  exclude_apps:\n  mtu: 1\n"); s != "tun:\n  "+want+"\n  mtu: 1\n" {
		t.Fatalf("空值：\n%q", s)
	}
	// 结果可被解析
	var c struct {
		TUN struct {
			ExcludeApps []string `yaml:"exclude_apps"`
		} `yaml:"tun"`
	}
	if err := yaml.Unmarshal([]byte(apply(config.Template)), &c); err != nil || len(c.TUN.ExcludeApps) != 2 || c.TUN.ExcludeApps[1] != "a b" {
		t.Fatalf("%v %+v", err, c)
	}
	// 嵌套结构无法自动改写
	if _, err := config.SetLists([]byte("tun:\n  exclude_apps:\n    - {a: 1}\n"), "tun", map[string][]string{"exclude_apps": nil}, []string{"exclude_apps"}); err == nil {
		t.Fatal("嵌套结构应报错")
	}
}
