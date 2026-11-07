package webui_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"testing/fstest"

	"github.com/Foreverwzh/velum/core/pkg/webui"
)

func setup(t *testing.T) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	api := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits.Add(1); _, _ = w.Write([]byte("api:" + r.URL.Path)) })
	assets := fstest.MapFS{"index.html": {Data: []byte("<title>ui</title>")}, "assets/a.js": {Data: []byte("js")}}
	s := httptest.NewServer(webui.Handler(api, assets, "secret"))
	t.Cleanup(s.Close)
	return s, &hits
}

func do(t *testing.T, s *httptest.Server, method, path, body, password string, hdr map[string]string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(method, s.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	if password != "" {
		req.SetBasicAuth("velum", password)
	}
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp
}

func TestFaviconNeedsNoPassword(t *testing.T) {
	s, _ := setup(t)
	resp := do(t, s, "GET", "/favicon.ico", "", "", nil)
	if resp.StatusCode != http.StatusNoContent || resp.Header.Get("WWW-Authenticate") != "" {
		t.Errorf("favicon 应直接答空：%d", resp.StatusCode)
	}
}

func TestPasswordRequired(t *testing.T) {
	s, hits := setup(t)
	for _, p := range []string{"/", "/assets/a.js", "/api/v1/status"} {
		resp := do(t, s, "GET", p, "", "", nil)
		if resp.StatusCode != 401 || resp.Header.Get("WWW-Authenticate") == "" {
			t.Errorf("%s 没有密码应为 401：%d", p, resp.StatusCode)
		}
	}
	if resp := do(t, s, "GET", "/", "", "wrong", nil); resp.StatusCode != 401 {
		t.Errorf("密码错误应为 401：%d", resp.StatusCode)
	}
	if hits.Load() != 0 {
		t.Error("未认证的请求不该到达 api")
	}
	if resp := do(t, s, "GET", "/", "", "secret", nil); resp.StatusCode != 200 {
		t.Errorf("首页：%d", resp.StatusCode)
	}
	if resp := do(t, s, "GET", "/api/v1/status", "", "secret", nil); resp.StatusCode != 200 || hits.Load() != 1 {
		t.Errorf("api：%d", resp.StatusCode)
	}
}

func TestCrossSiteRejected(t *testing.T) {
	s, hits := setup(t)
	for _, hdr := range []map[string]string{
		{"Sec-Fetch-Site": "cross-site"},
		{"Sec-Fetch-Site": "same-site"},
		{"Origin": "https://evil.example"},
	} {
		if resp := do(t, s, "POST", "/api/v1/reload", "", "secret", hdr); resp.StatusCode != 403 {
			t.Errorf("%v 应为 403：%d", hdr, resp.StatusCode)
		}
	}
	if hits.Load() != 0 {
		t.Error("跨站请求不该到达 api")
	}
	same := map[string]string{"Sec-Fetch-Site": "same-origin", "Origin": s.URL}
	if resp := do(t, s, "POST", "/api/v1/reload", "", "secret", same); resp.StatusCode != 200 {
		t.Errorf("同源请求：%d", resp.StatusCode)
	}
}

// 会断开服务器连接或让网页消失的操作要回到服务器上做。
func TestDangerousOperationsBlocked(t *testing.T) {
	s, hits := setup(t)
	for _, c := range []struct{ method, path, body string }{
		{"POST", "/api/v1/stop", ""},
		{"POST", "/api/v1/switches", `{"tun":"on"}`},
		{"POST", "/api/v1/switches", `{"tun":"try"}`},
		{"POST", "/api/v1/switches", `{"tun":"config"}`},
		{"GET", "/api/debug/pprof/profile", ""},
	} {
		if resp := do(t, s, c.method, c.path, c.body, "secret", nil); resp.StatusCode < 400 {
			t.Errorf("%s %s %s 应被拒绝：%d", c.method, c.path, c.body, resp.StatusCode)
		}
	}
	if hits.Load() != 0 {
		t.Error("这些请求不该到达 api")
	}
	if resp := do(t, s, "POST", "/api/v1/switches", `{"tun":"off"}`, "secret", nil); resp.StatusCode != 200 {
		t.Errorf("关闭 TUN 应当允许：%d", resp.StatusCode)
	}
}
