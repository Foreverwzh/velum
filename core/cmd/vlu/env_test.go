package main

import (
	"strings"
	"testing"
)

func TestProxyEnv(t *testing.T) {
	for _, c := range []struct{ listen, goos, want string }{
		{"127.0.0.1:7899", "linux", "export https_proxy=http://127.0.0.1:7899\n"},
		{"127.0.0.1:7899", "linux", "export ALL_PROXY=socks5://127.0.0.1:7899\n"},
		{"0.0.0.0:7899", "linux", "export http_proxy=http://127.0.0.1:7899\n"},
		{"[::]:7899", "linux", "export http_proxy=http://[::1]:7899\n"},
		{"10.0.0.2:7899", "linux", "export http_proxy=http://10.0.0.2:7899\n"},
		{"127.0.0.1:7899", "windows", "$env:HTTP_PROXY=\"http://127.0.0.1:7899\"\n"},
	} {
		if got := proxyEnv(c.listen, c.goos); !strings.Contains(got, c.want) {
			t.Errorf("proxyEnv(%q, %q) 缺少 %q：\n%s", c.listen, c.goos, c.want, got)
		}
	}
}
