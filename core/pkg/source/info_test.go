package source_test

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/Foreverwzh/velum/core/pkg/source"
)

func TestParseInfo(t *testing.T) {
	h := http.Header{}
	h.Set("Subscription-Userinfo", "upload=1073741824; download=2.5e9; total=107374182400; expire=1798732800")
	h.Set("Profile-Title", "base64:5py65Zy6IEE=") // 机场 A
	h.Set("Profile-Web-Page-Url", "https://example.com/user")
	in := source.ParseInfo(h)
	if in == nil || in.Upload != 1<<30 || in.Download != 2500000000 || in.Total != 100<<30 || !in.Expire.Equal(time.Unix(1798732800, 0)) ||
		in.Title != "机场 A" || in.WebPage != "https://example.com/user" || !in.HasUsage() {
		t.Fatalf("%+v", in)
	}

	h = http.Header{}
	h.Set("Content-Disposition", `attachment; filename*=UTF-8''%E6%9C%BA%E5%9C%BA%20B.yaml`)
	h.Set("Subscription-Userinfo", "upload=0; download=0; total=0; expire=0")
	if in := source.ParseInfo(h); in == nil || in.Title != "机场 B" || in.HasUsage() || !in.Expire.IsZero() {
		t.Fatalf("文件名作名称、没有额度：%+v", in)
	}
	if source.ParseInfo(http.Header{"Profile-Web-Page-Url": {"javascript:alert(1)"}}) != nil {
		t.Fatal("只接受 http(s) 用户中心地址")
	}
}

// 拉取时保存订阅信息，之后使用缓存时也能读到；响应中不再提供时删除，避免显示过时的额度。
func TestFetchSavesInfo(t *testing.T) {
	give := true
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if give {
			w.Header().Set("Subscription-Userinfo", "upload=1; download=2; total=10")
		}
		_, _ = w.Write([]byte("proxies: []"))
	}))
	defer srv.Close()
	f := source.New(filepath.Join(t.TempDir(), "cache"), "test")
	if _, _, err := f.Get("subscription", srv.URL, 0); err != nil {
		t.Fatal(err)
	}
	if in := f.Info("subscription", srv.URL); in == nil || in.Download != 2 || in.Total != 10 {
		t.Fatalf("%+v", in)
	}
	give = false
	_, _, _ = f.Get("subscription", srv.URL, 0)
	if in := f.Info("subscription", srv.URL); in != nil {
		t.Fatalf("应删除过时的信息：%+v", in)
	}
}
