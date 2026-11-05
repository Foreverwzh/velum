//go:build !android && !ios

package singbox

import (
	"os"
	"regexp"
	"strings"
	"testing"

	"github.com/sagernet/wireguard-go/device"
)

// 这个补丁决定了 WireGuard 流量下要占多少内存（third_party/wireguard-go/VELUM.md）。
// 升级依赖或替换模块时不能悄悄丢掉它。
func TestWireGuardPatch(t *testing.T) {
	if device.MaxSegmentSize > 4096 {
		t.Fatalf("MaxSegmentSize = %d：补丁丢失了，流量大时内存会涨到 200MB 以上", device.MaxSegmentSize)
	}
	mod, err := os.ReadFile("../../../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mod), "replace github.com/sagernet/wireguard-go => ./third_party/wireguard-go") {
		t.Fatal("go.mod 里没有 wireguard-go 的 replace")
	}
	base, err := os.ReadFile("../../../third_party/wireguard-go/.velum-base")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`github.com/sagernet/wireguard-go (v\S+)`).FindSubmatch(mod)
	if m == nil || string(m[1]) != strings.TrimSpace(string(base)) {
		t.Fatalf("go.mod 要求的版本和 third_party 的基线（%s）不一致：按 VELUM.md 重新应用补丁", strings.TrimSpace(string(base)))
	}
}
