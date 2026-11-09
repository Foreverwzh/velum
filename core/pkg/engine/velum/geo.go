package velum

import (
	"bufio"
	"bytes"
	"fmt"
	"net/netip"
	"strings"

	"github.com/Foreverwzh/velum/core/pkg/ir"
)

// 默认数据源（许可证均允许闭源再分发）：
//   - GEOIP：ipverse/rir-ip，CC0，来自各 RIR 的登记数据，每日更新。
//   - GEOSITE：v2fly/domain-list-community，MIT（分发时附版权声明）。
//
// 不用 sing-box 的 .srs：格式与数据都来自 GPL 项目，引擎也不解析它。
const (
	DefaultGeoIPURL   = "https://raw.githubusercontent.com/ipverse/rir-ip/master/country/{code}/ipv{v}-aggregated.txt"
	DefaultGeoSiteURL = "https://raw.githubusercontent.com/v2fly/domain-list-community/master/data/{name}"
)

// GeoSource 为 Velum 生成的 geoip-* / geosite-* 远程规则集提供纯文本数据。
type GeoSource struct {
	IPURL   string // 模板：{code} 国家代码（小写），{v} 为 4 / 6
	SiteURL string // 模板：{name} 列表名
	Fetch   func(url string) ([]byte, error)
}

// Load 按规则集标签取数据。不是 geoip-* / geosite-* 时返回 nil, nil。
func (g GeoSource) Load(tag string) ([]ir.Cond, error) {
	switch {
	case strings.HasPrefix(tag, "geoip-"):
		return g.geoip(strings.TrimPrefix(tag, "geoip-"))
	case strings.HasPrefix(tag, "geosite-"):
		return g.geosite(strings.TrimPrefix(tag, "geosite-"))
	}
	return nil, nil
}

func (g GeoSource) geoip(code string) ([]ir.Cond, error) {
	tpl := g.IPURL
	if tpl == "" {
		tpl = DefaultGeoIPURL
	}
	var cidrs []string
	for _, v := range []string{"4", "6"} {
		url := strings.NewReplacer("{code}", code, "{v}", v).Replace(tpl)
		b, err := g.Fetch(url)
		if err != nil {
			return nil, fmt.Errorf("geoip-%s：%w", code, err)
		}
		sc := bufio.NewScanner(bytes.NewReader(b))
		for sc.Scan() {
			l := strings.TrimSpace(sc.Text())
			if l == "" || strings.HasPrefix(l, "#") {
				continue
			}
			if _, err := netip.ParsePrefix(l); err == nil {
				cidrs = append(cidrs, l)
			}
		}
	}
	if len(cidrs) == 0 {
		return nil, fmt.Errorf("geoip-%s：数据为空", code)
	}
	return []ir.Cond{{IPCIDR: cidrs}}, nil
}

// geosite 解析 v2fly domain-list-community 的列表格式：
// include:名 / domain:x（后缀，不写前缀时同此）/ full:x（精确）/ keyword:x / regexp:x，行尾可带 @属性。
// 名字带 @属性（如 google@cn）时只取有该属性的条目。
func (g GeoSource) geosite(name string) ([]ir.Cond, error) {
	base, attr, _ := strings.Cut(name, "@")
	var c ir.Cond
	seen := map[string]bool{}
	var load func(list string, depth int) error
	load = func(list string, depth int) error {
		if seen[list] || depth > 16 {
			return nil // 循环或过深的 include
		}
		seen[list] = true
		tpl := g.SiteURL
		if tpl == "" {
			tpl = DefaultGeoSiteURL
		}
		b, err := g.Fetch(strings.ReplaceAll(tpl, "{name}", list))
		if err != nil {
			return fmt.Errorf("geosite-%s（%s）：%w", name, list, err)
		}
		sc := bufio.NewScanner(bytes.NewReader(b))
		for sc.Scan() {
			l := sc.Text()
			if i := strings.Index(l, "#"); i >= 0 {
				l = l[:i]
			}
			fields := strings.Fields(l)
			if len(fields) == 0 {
				continue
			}
			entry, attrs := fields[0], fields[1:]
			if inc, ok := strings.CutPrefix(entry, "include:"); ok {
				if err := load(inc, depth+1); err != nil {
					return err
				}
				continue
			}
			if attr != "" && !hasAttr(attrs, attr) {
				continue
			}
			kind, val, found := strings.Cut(entry, ":")
			if !found {
				kind, val = "domain", entry
			}
			val = strings.ToLower(val)
			switch kind {
			case "domain":
				c.DomainSuffix = append(c.DomainSuffix, val)
			case "full":
				c.Domain = append(c.Domain, val)
			case "keyword":
				c.DomainKeyword = append(c.DomainKeyword, val)
			case "regexp":
				c.DomainRegex = append(c.DomainRegex, entry[len("regexp:"):]) // 正则保持原样（大小写敏感）
			}
		}
		return sc.Err()
	}
	if err := load(base, 0); err != nil {
		return nil, err
	}
	if len(c.Domain)+len(c.DomainSuffix)+len(c.DomainKeyword)+len(c.DomainRegex) == 0 {
		return nil, fmt.Errorf("geosite-%s：没有条目", name)
	}
	return []ir.Cond{c}, nil
}

func hasAttr(attrs []string, want string) bool {
	for _, a := range attrs {
		if strings.TrimPrefix(a, "@") == want {
			return true
		}
	}
	return false
}
