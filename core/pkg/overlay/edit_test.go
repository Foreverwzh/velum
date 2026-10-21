package overlay

import (
	"strings"
	"testing"
)

const tmpl = `# Velum 本地覆盖层
#
# dev_bypass:
#   domains:
#     - corp.example.com
`

func TestAddRemoveKeepsComments(t *testing.T) {
	data := []byte(tmpl)
	var err error
	for _, e := range []Entry{
		{Kind: KindDomain, Value: "*.Corp.Example.com"},
		{Kind: KindCIDR, Value: "10.1.2.3/8"},
		{Kind: KindHost, Value: "dev.example.net", IP: "127.0.0.1"},
		{Kind: KindBypass, Value: "localhost"},
	} {
		if data, _, err = Add(data, e); err != nil {
			t.Fatal(err)
		}
	}
	s := string(data)
	if !strings.HasPrefix(s, "# Velum 本地覆盖层") {
		t.Fatalf("文件头注释丢失：\n%s", s)
	}
	o, err := Parse(data)
	if err != nil {
		t.Fatal(err)
	}
	if len(o.Domains) != 1 || o.Domains[0].Value != ".corp.example.com" || o.CIDRs[0].Value != "10.0.0.0/8" ||
		o.Hosts[0].IP.String() != "127.0.0.1" || o.SysProxyBypass[0].Value != "localhost" {
		t.Fatalf("%+v\n%s", o, s)
	}
	// 重复添加不改变内容；同名 hosts 更新地址
	if again, _, _ := Add(data, Entry{Kind: KindDomain, Value: ".corp.example.com"}); string(again) != s {
		t.Fatalf("重复添加改变了内容：\n%s", again)
	}
	if data, _, err = Add(data, Entry{Kind: KindHost, Value: "DEV.example.net", IP: "10.0.0.5"}); err != nil {
		t.Fatal(err)
	}
	if o, _ = Parse(data); len(o.Hosts) != 1 || o.Hosts[0].IP.String() != "10.0.0.5" {
		t.Fatalf("%+v", o.Hosts)
	}

	for _, e := range []Entry{{Kind: KindDomain, Value: ".corp.example.com"}, {Kind: KindCIDR, Value: "10.0.0.0/8"}, {Kind: KindHost, Value: "dev.example.net"}} {
		if data, err = Remove(data, e); err != nil {
			t.Fatal(err)
		}
	}
	s = string(data)
	if strings.Contains(s, "\ndev_bypass:") || !strings.Contains(s, "localhost") || !strings.HasPrefix(s, "# Velum") {
		t.Fatalf("删除后：\n%s", s)
	}
	if _, err := Remove(data, Entry{Kind: KindCIDR, Value: "192.168.0.0/16"}); err == nil {
		t.Fatal("删除不存在的条目应报错")
	}
}

func TestAddKeepsExistingLayout(t *testing.T) {
	src := "dev_bypass:\n  # 公司域名\n  domains: [corp.example.com]  # 行尾注释\n  cidrs:\n"
	out, _, err := Add([]byte(src), Entry{Kind: KindCIDR, Value: "10.0.0.0/8"})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "# 公司域名") || !strings.Contains(string(out), "# 行尾注释") {
		t.Fatalf("注释丢失：\n%s", out)
	}
	o, err := Parse(out)
	if err != nil || len(o.CIDRs) != 1 || len(o.Domains) != 1 {
		t.Fatalf("%v %+v\n%s", err, o, out)
	}
}

func TestAddRejectsInvalid(t *testing.T) {
	for _, e := range []Entry{{Kind: KindDomain, Value: "a b"}, {Kind: KindCIDR, Value: "nope"}, {Kind: KindHost, Value: "x.test", IP: "bad"}, {Kind: "x", Value: "y"}} {
		if _, _, err := Add(nil, e); err == nil {
			t.Errorf("%+v 应被拒绝", e)
		}
	}
}
