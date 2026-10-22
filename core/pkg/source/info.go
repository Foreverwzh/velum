package source

import (
	"encoding/base64"
	"encoding/json"
	"mime"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

// Info 是订阅服务在响应头中提供的信息（Clash 生态的约定）：流量额度、到期时间、名称。
// 随缓存保存（<缓存文件>.info），使用缓存时也能显示。
type Info struct {
	Upload   int64 `json:"upload"`
	Download int64 `json:"download"`
	// Total 是总额度（字节）；0 表示订阅没有提供额度。
	Total int64 `json:"total"`
	// Expire 是到期时间；零值表示没有提供（或不限期）。
	Expire  time.Time `json:"expire,omitzero"`
	Title   string    `json:"title,omitempty"`    // profile-title，没有时取 Content-Disposition 的文件名
	WebPage string    `json:"web_page,omitempty"` // profile-web-page-url（订阅服务的用户中心）
	// UpdatedAt 是获得这些信息的时间（即拉取时间）。
	UpdatedAt time.Time `json:"updated_at"`
}

// HasUsage 报告是否提供了流量额度。
func (i *Info) HasUsage() bool { return i != nil && i.Total > 0 }

// ParseInfo 从响应头解析订阅信息；没有任何可用信息时返回 nil。
func ParseInfo(h http.Header) *Info {
	in := &Info{UpdatedAt: time.Now()}
	ok := false
	if v := h.Get("Subscription-Userinfo"); v != "" {
		for _, kv := range strings.Split(v, ";") {
			k, val, found := strings.Cut(strings.TrimSpace(kv), "=")
			if !found {
				continue
			}
			// 部分服务输出小数或科学计数法
			f, err := strconv.ParseFloat(strings.TrimSpace(val), 64)
			if err != nil || f < 0 {
				continue
			}
			n := int64(f)
			switch strings.ToLower(strings.TrimSpace(k)) {
			case "upload":
				in.Upload, ok = n, true
			case "download":
				in.Download, ok = n, true
			case "total":
				in.Total, ok = n, true
			case "expire":
				if n > 0 {
					in.Expire, ok = time.Unix(n, 0), true
				}
			}
		}
	}
	if t := decodeTitle(h.Get("Profile-Title")); t != "" {
		in.Title, ok = t, true
	} else if _, params, err := mime.ParseMediaType(h.Get("Content-Disposition")); err == nil {
		// mime 已处理 filename*=UTF-8''… 形式
		if name := strings.TrimSpace(params["filename"]); name != "" {
			in.Title, ok = strings.TrimSuffix(strings.TrimSuffix(name, ".yaml"), ".yml"), true
		}
	}
	if u := strings.TrimSpace(h.Get("Profile-Web-Page-Url")); u != "" {
		if pu, err := url.Parse(u); err == nil && (pu.Scheme == "https" || pu.Scheme == "http") {
			in.WebPage, ok = u, true
		}
	}
	if !ok {
		return nil
	}
	return in
}

// decodeTitle 解码 profile-title（可能是 base64:… 形式）。
func decodeTitle(v string) string {
	v = strings.TrimSpace(v)
	if rest, found := strings.CutPrefix(v, "base64:"); found {
		if b, err := base64.StdEncoding.DecodeString(strings.TrimSpace(rest)); err == nil {
			return strings.TrimSpace(string(b))
		}
		return ""
	}
	return v
}

func infoPath(cachePath string) string { return cachePath + ".info" }

// saveInfo 保存本次拉取的订阅信息；响应中没有时删除旧的，避免显示过时的额度。
func saveInfo(cachePath string, h http.Header) {
	in := ParseInfo(h)
	if in == nil {
		_ = os.Remove(infoPath(cachePath))
		return
	}
	if data, err := json.Marshal(in); err == nil {
		_ = os.WriteFile(infoPath(cachePath), data, 0o600)
	}
}

// Info 返回某个 url 最近一次拉取时的订阅信息（没有时为 nil）。
func (f *Fetcher) Info(kind, rawURL string) *Info {
	data, err := os.ReadFile(infoPath(f.cachePath(kind, rawURL)))
	if err != nil {
		return nil
	}
	var in Info
	if json.Unmarshal(data, &in) != nil {
		return nil
	}
	return &in
}
