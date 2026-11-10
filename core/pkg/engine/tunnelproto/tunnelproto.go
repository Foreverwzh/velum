// Package tunnelproto 定义 iOS 上 App 与隧道扩展之间的约定：App 把编译好的配置写到共享目录，
// 扩展只加载它（扩展内存上限约 50MB，放不下订阅转换），其余经 App 消息查询。见 docs/IOS-PLAN.md。
package tunnelproto

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/Foreverwzh/velum/core/pkg/engine"
	"github.com/Foreverwzh/velum/core/pkg/i18n"
	"github.com/Foreverwzh/velum/core/pkg/ir"
)

// Dir 是 home 下的共享目录。
func Dir(home string) string { return filepath.Join(home, "tunnel") }

const (
	configFile   = "config.json"
	metaFile     = "meta.json"
	selectedFile = "selected.json"
)

// velum-engine 的文件（meta 里 engine 为 velum 时扩展使用）。App 编译出 velum-router.vrrt / velum-rules.vrdb
// 并把摘要写进 velum-compiled.txt；扩展发现编译产物缺失或摘要不符时自己编译。
const (
	EngineVelum       = "velum"
	VelumIRFile       = "velum-ir.json"
	VelumRuleSetsFile = "velum-rule-sets.json"
	VelumMetaFile     = "velum-meta.json"
)

// CachePath 是扩展使用的内核缓存：不与 App 内的本地引擎共用（bbolt 独占文件）。
func CachePath(home string) string { return filepath.Join(Dir(home), "cache.db") }

// LogPath 是扩展的内核日志。
func LogPath(home string) string { return filepath.Join(Dir(home), "tunnel.log") }

// meta 是引擎配置中内核 JSON 以外的部分。
type meta struct {
	RuleSources []ir.Source              `json:"rule_sources,omitempty"`
	RuleIndex   []int                    `json:"rule_index,omitempty"`
	LogLevel    string                   `json:"log_level,omitempty"`
	Routes      []string                 `json:"routes,omitempty"`
	SystemDNS   []string                 `json:"system_dns,omitempty"`
	Policies    map[string]*ir.NetPolicy `json:"policies,omitempty"`
	// Engine 是扩展使用的内核：空为 sing-box，velum 为 velum-engine。
	Engine string `json:"engine,omitempty"`
	Hash   string `json:"hash"`
}

func hashOf(c engine.Config, routes, dns []string) string {
	h := sha256.New()
	h.Write(c.JSON)
	b, _ := json.Marshal([][]string{routes, dns})
	h.Write(b)
	b, _ = json.Marshal(c.Policies)
	h.Write(b)
	if v := c.Velum; v != nil {
		for _, part := range [][]byte{[]byte(EngineVelum), v.IR, v.RuleSets, v.Meta} {
			_, _ = fmt.Fprintf(h, "%d:", len(part))
			h.Write(part)
		}
	}
	return hex.EncodeToString(h.Sum(nil))
}

// Save 写入扩展要加载的配置，返回其摘要。先写配置、最后写 meta：扩展读到新 meta 时配置一定已就绪。
func Save(home string, c engine.Config) (string, error) {
	dir := Dir(home)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	var routes, dns []string
	if c.TUN != nil {
		routes, dns = c.TUN.Routes, c.TUN.SystemDNS
	}
	m := meta{RuleSources: c.RuleSources, RuleIndex: c.RuleIndex, LogLevel: c.LogLevel, Routes: routes, SystemDNS: dns, Policies: c.Policies, Hash: hashOf(c, routes, dns)}
	if c.Velum != nil {
		m.Engine = EngineVelum
	}
	b, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	if err := writeAtomic(filepath.Join(dir, configFile), c.JSON); err != nil {
		return "", err
	}
	if v := c.Velum; v != nil {
		for name, data := range map[string][]byte{VelumIRFile: v.IR, VelumRuleSetsFile: v.RuleSets, VelumMetaFile: v.Meta} {
			if err := writeAtomic(filepath.Join(dir, name), data); err != nil {
				return "", err
			}
		}
	}
	return m.Hash, writeAtomic(filepath.Join(dir, metaFile), b)
}

// LocalDir 是 App 内本地引擎（velum-engine 本地模式，没开隧道时用）的配置目录。
func LocalDir(home string) string { return filepath.Join(home, "local") }

// SaveLocal 写入本地引擎的配置（velum-engine）：文件与隧道目录相同（meta.json + velum-*.json），由 App 的 Swift 编译、启动。
// 返回摘要。c.Velum 必须非空。
func SaveLocal(home string, c engine.Config) (string, error) {
	v := c.Velum
	if v == nil {
		return "", i18n.New("本地引擎需要 velum-engine 的配置")
	}
	dir := LocalDir(home)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	m := meta{Engine: EngineVelum, Policies: c.Policies, Hash: hashOf(c, nil, nil)}
	b, err := json.Marshal(m)
	if err != nil {
		return "", err
	}
	for name, data := range map[string][]byte{VelumIRFile: v.IR, VelumRuleSetsFile: v.RuleSets, VelumMetaFile: v.Meta} {
		if err := writeAtomic(filepath.Join(dir, name), data); err != nil {
			return "", err
		}
	}
	return m.Hash, writeAtomic(filepath.Join(dir, metaFile), b)
}

// SavedHash 返回当前已写入配置的摘要；没有时为空。
func SavedHash(home string) string {
	var m meta
	b, err := os.ReadFile(filepath.Join(Dir(home), metaFile))
	if err != nil || json.Unmarshal(b, &m) != nil {
		return ""
	}
	return m.Hash
}

// Load 读取配置。opener 由扩展提供。
func Load(home string, opener engine.TUNOpener) (engine.Config, error) {
	dir := Dir(home)
	var c engine.Config
	var m meta
	b, err := os.ReadFile(filepath.Join(dir, metaFile))
	if err != nil {
		return c, err
	}
	if err := json.Unmarshal(b, &m); err != nil {
		return c, err
	}
	if c.JSON, err = os.ReadFile(filepath.Join(dir, configFile)); err != nil {
		return c, err
	}
	c.RuleSources, c.RuleIndex, c.LogLevel, c.CachePath, c.Policies = m.RuleSources, m.RuleIndex, m.LogLevel, CachePath(home), m.Policies
	c.TUN = &engine.TUNConfig{Routes: m.Routes, SystemDNS: m.SystemDNS, Opener: opener}
	return c, nil
}

// SaveSelected 记录用户的选择：配置渲染之后的切换不在配置里，扩展被系统单独拉起时要靠它恢复。
func SaveSelected(home string, sel map[string]string) error {
	if err := os.MkdirAll(Dir(home), 0o700); err != nil {
		return err
	}
	b, err := json.Marshal(sel)
	if err != nil {
		return err
	}
	return writeAtomic(filepath.Join(Dir(home), selectedFile), b)
}

// LoadSelected 读取用户的选择。
func LoadSelected(home string) map[string]string {
	var sel map[string]string
	if b, err := os.ReadFile(filepath.Join(Dir(home), selectedFile)); err == nil {
		_ = json.Unmarshal(b, &sel)
	}
	return sel
}

func writeAtomic(path string, data []byte) error {
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// 操作名。
const (
	OpReload  = "reload"
	OpState   = "state"
	OpConns   = "connections"
	OpSelect  = "select"
	OpTest    = "test"
	OpResolve = "resolve"
)

// Request 是 App 发给扩展的消息。
type Request struct {
	Op     string `json:"op"`
	Group  string `json:"group,omitempty"`
	Member string `json:"member,omitempty"`
	Limit  int    `json:"limit,omitempty"`
	Domain string `json:"domain,omitempty"`
}

// Response 是扩展的回复；Error 非空表示失败，其余字段按操作填写。
type Response struct {
	Error       string              `json:"error,omitempty"`
	State       *State              `json:"state,omitempty"`
	Connections []engine.Connection `json:"connections,omitempty"`
	Delays      map[string]int      `json:"delays,omitempty"`
	Addrs       []string            `json:"addrs,omitempty"`
}

// State 是一次查询到的引擎状态。界面每 2 秒刷新、策略组成员可达数百，逐项经 App 消息查询不现实，所以一次取回。
type State struct {
	Selected map[string]string `json:"selected,omitempty"`
	Delays   map[string]int    `json:"delays,omitempty"`
	Traffic  engine.Traffic    `json:"traffic"`
	TUNName  string            `json:"tun_name,omitempty"`
}
