// Package source 负责获取订阅与 provider 内容：远程拉取 + 本地缓存，失败时回落缓存。
package source

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Foreverwzh/velum/core/pkg/ir"
	"github.com/Foreverwzh/velum/core/pkg/userfs"
)

type Fetcher struct {
	CacheDir  string
	UserAgent string
	Client    *http.Client
	// PreferLocal 为 true 时，provider 本地文件存在即直接使用。
	PreferLocal bool
	// TTL 内的缓存直接使用，不发请求。
	TTL time.Duration
	// Stale 为 true 时，缓存存在即直接使用（即使已过期），只有没有缓存才拉取。
	// 用于启动：先用缓存立即可用，过期的内容随后在后台更新（与 Clash / Stash 一致）。
	Stale bool
	// Log 非空时记录较慢（超过 3 秒）或失败的远程请求，只含主机名（订阅地址可能带 token）。
	Log func(format string, args ...any)
	// Fallback 非空时，直连失败（网络错误，不含 HTTP 错误码）后经这个代理重试，一般是 Velum 自己的本地端口：
	// 首次设置时还没有可用的代理，GitHub 上的规则集等可能直连不通。
	Fallback  *url.URL
	usedStale atomic.Bool
}

const slowFetch = 3 * time.Second

// UsedStale 报告本次是否使用了过期缓存（需要在后台更新）。
func (f *Fetcher) UsedStale() bool { return f.usedStale.Load() }

// Result 记录一次获取的来源，用于 vlu status 展示与告警。
type Result struct {
	Origin    string    `json:"origin"` // remote / cache / file
	FetchedAt time.Time `json:"fetched_at"`
	Err       string    `json:"error,omitempty"` // 远程失败但回落缓存时的错误
}

func New(cacheDir, ua string) *Fetcher {
	return &Fetcher{
		CacheDir:  cacheDir,
		UserAgent: ua,
		// 遵循 HTTP(S)_PROXY 环境变量；Velum 不写死任何上游。
		Client: newClient(http.ProxyFromEnvironment),
		TTL:    time.Hour,
	}
}

// newClient 的连接、握手、等待响应头分别限时：被墙或不通的地址（常见于还没有代理可用的首次设置）尽快失败，
// 整体 30 秒只留给下载较大的内容。
func newClient(proxy func(*http.Request) (*url.URL, error)) *http.Client {
	return &http.Client{Timeout: 30 * time.Second, Transport: &http.Transport{
		Proxy:                 proxy,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
		ForceAttemptHTTP2:     true,
	}}
}

// CachePath 返回某个 url 在缓存中的文件路径。
func (f *Fetcher) CachePath(kind, rawURL string) string { return f.cachePath(kind, rawURL) }

func (f *Fetcher) cachePath(kind, rawURL string) string {
	h := sha256.Sum256([]byte(rawURL))
	return filepath.Join(f.CacheDir, kind, hex.EncodeToString(h[:8]))
}

// Get 拉取 url；TTL 内或失败时使用缓存。
func (f *Fetcher) Get(kind, rawURL string, ttl time.Duration) ([]byte, Result, error) {
	cp := f.cachePath(kind, rawURL)
	if st, err := os.Stat(cp); err == nil && ttl > 0 && (f.Stale || time.Since(st.ModTime()) < ttl) {
		data, err := os.ReadFile(cp)
		if err == nil {
			if time.Since(st.ModTime()) >= ttl {
				f.usedStale.Store(true)
			}
			return data, Result{Origin: "cache", FetchedAt: st.ModTime()}, nil
		}
	}
	start := time.Now()
	data, err := f.fetch(f.Client, rawURL)
	if err != nil && f.Fallback != nil && !errors.As(err, new(statusError)) {
		if data, err = f.fetch(newClient(http.ProxyURL(f.Fallback)), rawURL); err == nil && f.Log != nil {
			f.Log("直连拉取 %s 失败，已经 Velum 代理拉取", kind)
		}
	}
	if took := time.Since(start); f.Log != nil && (err != nil || took > slowFetch) {
		host := rawURL
		if u, perr := url.Parse(rawURL); perr == nil {
			host = u.Host
		}
		if err != nil {
			f.Log("拉取 %s（%s）失败，用时 %.1fs：%v", kind, host, took.Seconds(), redactErr(err, rawURL, host))
		} else {
			f.Log("拉取 %s（%s）较慢，用时 %.1fs", kind, host, took.Seconds())
		}
	}
	if err == nil {
		_ = os.MkdirAll(filepath.Dir(cp), 0o700)
		_ = os.WriteFile(cp, data, 0o600)
		return data, Result{Origin: "remote", FetchedAt: time.Now()}, nil
	}
	if cached, cerr := os.ReadFile(cp); cerr == nil {
		st, _ := os.Stat(cp)
		return cached, Result{Origin: "cache", FetchedAt: st.ModTime(), Err: err.Error()}, nil
	}
	return nil, Result{Err: err.Error()}, err
}

// redactErr 去掉错误信息中的完整地址（url.Error 会带上它，可能含 token）。
func redactErr(err error, rawURL, host string) string {
	if ue, ok := err.(*url.Error); ok && ue.URL == rawURL {
		return ue.Op + " " + host + ": " + ue.Err.Error()
	}
	return err.Error()
}

type statusError int

func (e statusError) Error() string { return fmt.Sprintf("HTTP %d", int(e)) }

func (f *Fetcher) fetch(client *http.Client, rawURL string) ([]byte, error) {
	req, err := http.NewRequest(http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, err
	}
	if f.UserAgent != "" {
		req.Header.Set("User-Agent", f.UserAgent)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, statusError(resp.StatusCode)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 64<<20))
}

// ProviderReader 返回供 subscription.Options.ReadFile 使用的读取函数。
//
// 与 Clash 语义一致：provider 的 path 是它的本地缓存。
//   - 只有 path：读本地文件；
//   - PreferLocal 且本地文件存在：直接使用；
//   - 否则按 url 拉取（Velum 缓存 TTL 内不重复请求）；失败时回落 Velum 缓存，再回落 path 文件。
func (f *Fetcher) ProviderReader() func(path, url string) ([]byte, error) {
	return func(path, url string) ([]byte, error) {
		if url == "" {
			return userfs.ReadFile(path)
		}
		if f.PreferLocal {
			if data, err := userfs.ReadFile(path); err == nil {
				return data, nil
			}
		}
		data, _, err := f.Get("providers", url, f.TTL)
		if err == nil {
			return data, nil
		}
		if local, lerr := userfs.ReadFile(path); lerr == nil {
			return local, nil
		}
		return nil, err
	}
}

// ResolveRemoteRuleSets 下载 cfg 中的远程规则集（GeoIP / GeoSite 等），转为本地文件规则集。
//
// 由 Velum 而非内核下载：遵循 HTTP(S)_PROXY、带缓存、失败回落缓存；
// 下载失败且无缓存时，该规则集降级为空集（相关规则不命中）并告警，不影响整体启动
// （sing-box 远程规则集首次下载失败会导致整个引擎无法启动）。
func (f *Fetcher) ResolveRemoteRuleSets(cfg *ir.Config, ttl time.Duration) []ir.Warning {
	type got struct {
		res Result
		err error
	}
	results := make([]got, len(cfg.RuleSets))
	var wg sync.WaitGroup
	for i := range cfg.RuleSets {
		if cfg.RuleSets[i].Kind != ir.RuleSetRemote {
			continue
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, results[i].res, results[i].err = f.Get("rulesets", cfg.RuleSets[i].URL, ttl)
		}()
	}
	wg.Wait()
	var ws []ir.Warning
	for i := range cfg.RuleSets {
		rs := &cfg.RuleSets[i]
		if rs.Kind != ir.RuleSetRemote {
			continue
		}
		res, err := results[i].res, results[i].err
		if err != nil {
			ws = append(ws, ir.Warning{Category: "ruleset", Kind: rs.Tag, Level: ir.Unsupported,
				Reason: "下载失败且无缓存，规则集暂按空集处理（相关规则不会命中）：" + err.Error(), Subject: rs.URL, Source: rs.Source})
			rs.Kind, rs.Rules = ir.RuleSetInline, nil
			continue
		}
		if res.Err != "" {
			ws = append(ws, ir.Warning{Category: "ruleset", Kind: rs.Tag, Level: ir.Degraded,
				Reason: "下载失败，使用 " + res.FetchedAt.Format("01-02 15:04") + " 的缓存：" + res.Err, Subject: rs.URL, Source: rs.Source})
		}
		rs.Kind, rs.Path = ir.RuleSetFile, f.CachePath("rulesets", rs.URL)
		if rs.Format == "" {
			rs.Format = "binary"
		}
	}
	return ws
}
