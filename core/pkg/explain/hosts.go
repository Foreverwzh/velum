package explain

import (
	"bufio"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"sort"
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

// HostsEntry 是系统 hosts 文件中的一个域名及其地址。
type HostsEntry struct {
	Name  string   `json:"name"`
	Addrs []string `json:"addrs"`
}

// SystemHostsEntries 列出系统 hosts 文件中的条目（不含 localhost 等系统默认项），最多 limit 个，按名称排序。
var SystemHostsEntries = func(limit int) (entries []HostsEntry, total int) { return sysHosts.entries(limit) }

func (c *hostsCache) entries(limit int) ([]HostsEntry, int) {
	c.lookup("") // 按需重新读取
	c.mu.Lock()
	defer c.mu.Unlock()
	var names []string
	for n := range c.byName {
		switch n {
		case "localhost", "broadcasthost", "ip6-localhost", "ip6-loopback", "localhost.localdomain":
			continue
		}
		names = append(names, n)
	}
	sort.Strings(names)
	total := len(names)
	if len(names) > limit {
		names = names[:limit]
	}
	out := make([]HostsEntry, 0, len(names))
	for _, n := range names {
		e := HostsEntry{Name: n}
		for _, a := range c.byName[n] {
			e.Addrs = append(e.Addrs, a.String())
		}
		out = append(out, e)
	}
	return out, total
}
