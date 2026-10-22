// Package update 在 GitHub Releases 上查询 Velum 的新版本。
package update

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// ReleasesAPI 是发布列表接口（按发布时间倒序）。
const ReleasesAPI = "https://api.github.com/repos/Foreverwzh/velum/releases?per_page=20"

type Release struct {
	Version   string    // 不带 v，如 0.1.0-rc.6
	URL       string    // 发布页
	Download  string    // 当前平台的安装包；没有时为空
	Published time.Time // 发布时间
}

// AssetSuffix 是某个平台安装包文件名的结尾（与 .github/workflows/desktop.yml 的命名一致）；不支持的平台返回空。
func AssetSuffix(goos, goarch string) string {
	switch goos + "/" + goarch {
	case "darwin/arm64":
		return "-macos-arm64.zip"
	case "windows/amd64":
		return "-windows-x64-setup.exe"
	}
	return ""
}

// Latest 返回版本号最高的发布；prerelease 为 false 时不考虑预发布版本。asset 是安装包文件名结尾（见 AssetSuffix）。
func Latest(ctx context.Context, client *http.Client, api, ua string, prerelease bool, asset string) (*Release, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", ua)
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub 返回 %s", resp.Status)
	}
	var list []struct {
		Tag        string    `json:"tag_name"`
		URL        string    `json:"html_url"`
		Draft      bool      `json:"draft"`
		Prerelease bool      `json:"prerelease"`
		Published  time.Time `json:"published_at"`
		Assets     []struct {
			Name string `json:"name"`
			URL  string `json:"browser_download_url"`
		} `json:"assets"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 8<<20)).Decode(&list); err != nil {
		return nil, fmt.Errorf("解析发布列表：%w", err)
	}
	var best *Release
	for _, r := range list {
		v := strings.TrimPrefix(r.Tag, "v")
		if r.Draft || (r.Prerelease && !prerelease) || parse(v) == nil {
			continue
		}
		if best != nil && !Newer(v, best.Version) {
			continue
		}
		best = &Release{Version: v, URL: r.URL, Published: r.Published}
		for _, a := range r.Assets {
			if asset != "" && strings.HasSuffix(a.Name, asset) {
				best.Download = a.URL
			}
		}
	}
	if best == nil {
		return nil, fmt.Errorf("没有找到可用的发布")
	}
	return best, nil
}

// Newer 报告版本 a 是否比 b 新（语义化版本：预发布版本低于对应的正式版本，预发布标识中的数字按数值比较）。
// 无法解析的版本视为最旧。
func Newer(a, b string) bool {
	return compare(parse(strings.TrimPrefix(a, "v")), parse(strings.TrimPrefix(b, "v"))) > 0
}

// IsPrerelease 报告版本是否为预发布版本（如 0.1.0-rc.6、0.1.0-dev）。
func IsPrerelease(v string) bool {
	p := parse(strings.TrimPrefix(v, "v"))
	return p != nil && len(p.pre) > 0
}

type version struct {
	core [3]int
	pre  []string
}

func parse(s string) *version {
	s, _, _ = strings.Cut(s, "+") // 构建元数据不参与比较
	main, pre, hasPre := strings.Cut(s, "-")
	parts := strings.Split(main, ".")
	if len(parts) != 3 {
		return nil
	}
	v := &version{}
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return nil
		}
		v.core[i] = n
	}
	if hasPre {
		v.pre = strings.Split(pre, ".")
	}
	return v
}

func compare(a, b *version) int {
	switch {
	case a == nil && b == nil:
		return 0
	case a == nil:
		return -1
	case b == nil:
		return 1
	}
	for i := range a.core {
		if a.core[i] != b.core[i] {
			return cmpInt(a.core[i], b.core[i])
		}
	}
	switch {
	case len(a.pre) == 0 && len(b.pre) == 0:
		return 0
	case len(a.pre) == 0:
		return 1
	case len(b.pre) == 0:
		return -1
	}
	for i := 0; i < len(a.pre) && i < len(b.pre); i++ {
		x, ex := strconv.Atoi(a.pre[i])
		y, ey := strconv.Atoi(b.pre[i])
		switch {
		case ex == nil && ey == nil:
			if x != y {
				return cmpInt(x, y)
			}
		case ex == nil: // 数字标识低于字母标识
			return -1
		case ey == nil:
			return 1
		case a.pre[i] != b.pre[i]:
			return strings.Compare(a.pre[i], b.pre[i])
		}
	}
	return cmpInt(len(a.pre), len(b.pre))
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}
