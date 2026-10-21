package explain

import (
	"bufio"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// SystemHosts 查询系统 hosts 文件中的地址（与引擎的 hosts DNS 服务器读同一个文件）；测试可替换。
var SystemHosts = func(domain string) []netip.Addr { return sysHosts.lookup(domain) }

var sysHosts hostsCache

// hostsCache 按修改时间缓存解析结果，文件变化后重新读取。
type hostsCache struct {
	mu     sync.Mutex
	mod    time.Time
	size   int64
	byName map[string][]netip.Addr
}

func hostsPath() string {
	if runtime.GOOS == "windows" {
		return filepath.Join(os.Getenv("SystemRoot"), "System32", "drivers", "etc", "hosts")
	}
	return "/etc/hosts"
}

func (c *hostsCache) lookup(domain string) []netip.Addr {
	c.mu.Lock()
	defer c.mu.Unlock()
	path := hostsPath()
	st, err := os.Stat(path)
	if err != nil {
		return nil
	}
	if c.byName == nil || !st.ModTime().Equal(c.mod) || st.Size() != c.size {
		f, err := os.Open(path)
		if err != nil {
			return nil
		}
		c.byName = parseHosts(f)
		f.Close()
		c.mod, c.size = st.ModTime(), st.Size()
	}
	return c.byName[strings.TrimSuffix(strings.ToLower(domain), ".")]
}

func parseHosts(f *os.File) map[string][]netip.Addr {
	m := map[string][]netip.Addr{}
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line, _, _ := strings.Cut(sc.Text(), "#")
		fs := strings.Fields(line)
		if len(fs) < 2 {
			continue
		}
		a, err := netip.ParseAddr(fs[0])
		if err != nil {
			continue
		}
		a = a.WithZone("")
		for _, name := range fs[1:] {
			name = strings.TrimSuffix(strings.ToLower(name), ".")
			m[name] = append(m[name], a)
		}
	}
	return m
}
