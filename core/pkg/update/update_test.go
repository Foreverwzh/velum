package update

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewer(t *testing.T) {
	for _, c := range []struct {
		a, b string
		want bool
	}{
		{"0.1.0-rc.10", "0.1.0-rc.9", true},
		{"0.1.0", "0.1.0-rc.9", true},
		{"0.1.0-rc.9", "0.1.0", false},
		{"0.2.0-rc.1", "0.1.9", true},
		{"v0.1.1", "0.1.0", true},
		{"0.1.0-rc.6", "0.1.0-rc.6", false},
		{"0.1.0-rc.6", "0.1.0-dev", true},
		{"0.1.0-rc.1.1", "0.1.0-rc.1", true},
		{"0.1.0", "garbage", true},
		{"garbage", "0.1.0", false},
	} {
		if got := Newer(c.a, c.b); got != c.want {
			t.Errorf("Newer(%q, %q) = %v，期望 %v", c.a, c.b, got, c.want)
		}
	}
}

func TestLatest(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`[
			{"tag_name":"v0.2.0-rc.1","html_url":"https://example.com/rc","prerelease":true,
			 "assets":[{"name":"Velum-0.2.0-rc.1-macos-arm64.zip","browser_download_url":"https://example.com/rc.zip"}]},
			{"tag_name":"v0.3.0","draft":true},
			{"tag_name":"v0.1.0-rc.10","html_url":"https://example.com/old","prerelease":true},
			{"tag_name":"v0.1.1","html_url":"https://example.com/stable",
			 "assets":[{"name":"Velum-0.1.1-windows-x64-setup.exe","browser_download_url":"https://example.com/stable.exe"}]}
		]`))
	}))
	defer srv.Close()
	ctx := context.Background()

	r, err := Latest(ctx, srv.Client(), srv.URL, "test", true, AssetSuffix("darwin", "arm64"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Version != "0.2.0-rc.1" || r.URL != "https://example.com/rc" || r.Download != "https://example.com/rc.zip" {
		t.Fatalf("含预发布：%+v", r)
	}

	r, err = Latest(ctx, srv.Client(), srv.URL, "test", false, AssetSuffix("windows", "amd64"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Version != "0.1.1" || r.Download != "https://example.com/stable.exe" {
		t.Fatalf("仅正式版：%+v", r)
	}
}
