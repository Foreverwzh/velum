package velum

import (
	"fmt"
	"slices"
	"testing"
)

func fakeFetch(files map[string]string) func(string) ([]byte, error) {
	return func(url string) ([]byte, error) {
		if b, ok := files[url]; ok {
			return []byte(b), nil
		}
		return nil, fmt.Errorf("404 %s", url)
	}
}

func TestGeoSiteParsesV2flyFormat(t *testing.T) {
	g := GeoSource{SiteURL: "s/{name}", Fetch: fakeFetch(map[string]string{
		"s/google":  "# 注释\ngoogle.com\nfull:www.google.cn @cn\nkeyword:googlevideo\nregexp:^gg\\d+\\.example$\ninclude:youtube\ninclude:google # 循环\n",
		"s/youtube": "domain:youtube.com\nfull:yt.example @cn @ads\n",
	})}
	c, err := g.Load("geosite-google")
	if err != nil {
		t.Fatal(err)
	}
	got := c[0]
	if !slices.Contains(got.DomainSuffix, "google.com") || !slices.Contains(got.DomainSuffix, "youtube.com") {
		t.Errorf("后缀（含 include）：%v", got.DomainSuffix)
	}
	if !slices.Contains(got.Domain, "www.google.cn") || got.DomainKeyword[0] != "googlevideo" || got.DomainRegex[0] != `^gg\d+\.example$` {
		t.Errorf("full / keyword / regexp：%+v", got)
	}
	cn, err := g.Load("geosite-google@cn")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(cn[0].Domain, []string{"www.google.cn", "yt.example"}) || len(cn[0].DomainSuffix) != 0 {
		t.Errorf("@cn 只取带属性的条目：%+v", cn[0])
	}
	if _, err := g.Load("geosite-missing"); err == nil {
		t.Error("列表不存在应当报错")
	}
	if c, err := g.Load("other"); c != nil || err != nil {
		t.Error("不是 geo 规则集应当返回 nil, nil")
	}
}

func TestGeoIPMergesV4AndV6(t *testing.T) {
	g := GeoSource{IPURL: "ip/{code}/{v}", Fetch: fakeFetch(map[string]string{
		"ip/cn/4": "# x\n1.0.1.0/24\nbad\n",
		"ip/cn/6": "2001:250::/35\n",
	})}
	c, err := g.Load("geoip-cn")
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(c[0].IPCIDR, []string{"1.0.1.0/24", "2001:250::/35"}) {
		t.Errorf("%v", c[0].IPCIDR)
	}
}
