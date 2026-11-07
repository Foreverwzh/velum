// Package webui 把桌面端的界面（desktop/dist）作为网页提供，供没有桌面的 Linux 服务器使用。
//
// 带 webui 构建标签时内嵌界面，见 embed.go；否则只有一个说明页。
package webui

import (
	"bytes"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"path"
	"strings"
	"time"

	"github.com/Foreverwzh/velum/core/pkg/i18n"
	"github.com/Foreverwzh/velum/core/pkg/rpc"
)

// failDelay 是密码错误后的等待时间，让暴力猜测变慢。
const failDelay = time.Second

// Handler 用 HTTP Basic 认证（用户名不限）保护界面和 api。认证信息由浏览器自动带上，所以 api 还要拒绝跨站请求。
func Handler(api http.Handler, assets fs.FS, password string) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/api/", http.StripPrefix("/api", guardAPI(api)))
	mux.Handle("/", static(assets))
	return secure(mux, password)
}

func secure(next http.Handler, password string) http.Handler {
	want := []byte(password)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 浏览器请求图标时可能不带认证信息。界面没有图标，直接答空，不用 401 触发多余的认证。
		if r.URL.Path == "/favicon.ico" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		_, got, _ := r.BasicAuth()
		if subtle.ConstantTimeCompare([]byte(got), want) != 1 {
			if got != "" {
				time.Sleep(failDelay)
			}
			w.Header().Set("WWW-Authenticate", `Basic realm="Velum", charset="UTF-8"`)
			http.Error(w, http.StatusText(http.StatusUnauthorized), http.StatusUnauthorized)
			return
		}
		h := w.Header()
		h.Set("X-Frame-Options", "DENY")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Referrer-Policy", "no-referrer")
		h.Set("Content-Security-Policy", "default-src 'self'; style-src 'self' 'unsafe-inline'; img-src 'self' data:; frame-ancestors 'none'")
		next.ServeHTTP(w, r)
	})
}

// guardAPI 拒绝跨站请求和不适合从网页发起的操作。
func guardAPI(api http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if crossSite(r) {
			deny(w, http.StatusForbidden, errors.New(i18n.T("拒绝跨站请求")))
			return
		}
		switch {
		case strings.HasPrefix(r.URL.Path, "/debug/"):
			http.NotFound(w, r)
			return
		case r.Method == http.MethodPost && r.URL.Path == "/v1/stop":
			// 停掉核心后网页也没了，只能回到服务器上恢复
			deny(w, http.StatusForbidden, errors.New(i18n.T("网页界面不能停止 Velum：请在服务器上执行 sudo velumd service stop")))
			return
		case r.Method == http.MethodPost && r.URL.Path == "/v1/switches":
			body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<16))
			if err != nil {
				deny(w, http.StatusBadRequest, err)
				return
			}
			var req rpc.SwitchRequest
			if err := json.Unmarshal(body, &req); err != nil {
				deny(w, http.StatusBadRequest, err)
				return
			}
			// 开启 TUN 可能断开 SSH 和这个网页，要走带自动回退的试运行，而回退定时器由命令行设置
			if req.TUN != "" && req.TUN != "off" {
				deny(w, http.StatusForbidden, errors.New(i18n.T("网页界面不能开启 TUN，以免断开 SSH 和网页：请在服务器上执行 sudo vlu tun on --try")))
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))
		}
		api.ServeHTTP(w, r)
	})
}

// crossSite 判断请求是否来自别的网站。Sec-Fetch-Site 由浏览器设置，网页改不了；旧浏览器没有它时比较 Origin 和 Host。
func crossSite(r *http.Request) bool {
	switch r.Header.Get("Sec-Fetch-Site") {
	case "same-origin", "none":
		return false
	case "":
	default:
		return true
	}
	origin := r.Header.Get("Origin")
	if origin == "" {
		return false // 不是浏览器发起的（curl 等），或是同源的 GET
	}
	u, err := url.Parse(origin)
	return err != nil || u.Host != r.Host
}

func deny(w http.ResponseWriter, status int, err error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": err.Error()})
}

// static 提供界面文件。带哈希的资源可以长期缓存，入口页不缓存，否则升级后还会加载旧资源。
func static(assets fs.FS) http.Handler {
	files := http.FileServerFS(assets)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/")
		if strings.HasPrefix(p, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		} else {
			w.Header().Set("Cache-Control", "no-store")
		}
		if p == "favicon.ico" {
			w.WriteHeader(http.StatusNoContent) // 浏览器会自动请求，界面没有这个文件
			return
		}
		if p == "" || p == "index.html" {
			http.ServeFileFS(w, r, assets, "index.html")
			return
		}
		files.ServeHTTP(w, r)
	})
}
