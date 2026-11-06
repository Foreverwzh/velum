//go:build linux && !android

package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"flag"
	"fmt"
	"github.com/Foreverwzh/velum/core/pkg/i18n"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/Foreverwzh/velum/core/pkg/rpc"
	"github.com/Foreverwzh/velum/core/pkg/update"
)

const upgradeHint = "sudo vlu upgrade"

const maxPackage = 256 << 20

// upgrade 下载并校验新版本后替换服务（docs/ML-PLAN.md）。不做后台自动更新：服务器不应自行改变运行中的程序。
// 校验顺序：SHA256SUMS 的签名（确认来自 Velum、版本正确）→ 压缩包的校验和。在校验通过前不解压、不执行其中任何东西。
// 新版本起不来时自动装回旧版本。
func upgrade(c *rpc.Client, args []string) error {
	fs := flag.NewFlagSet("upgrade", flag.ContinueOnError)
	version := fs.String("version", "", i18n.T("升级到指定版本（可以比当前版本旧）"))
	beta := fs.Bool("beta", false, i18n.T("包括预发布版本"))
	file := fs.String("file", "", i18n.T("用本地压缩包升级（同目录要有 SHA256SUMS 和 SHA256SUMS.sig）"))
	if err := fs.Parse(args); err != nil {
		return err
	}
	if os.Geteuid() != 0 {
		return i18n.New("升级要替换程序并重启服务，需要 root：sudo vlu upgrade")
	}
	if _, err := os.Stat(linuxServiceUnit); err != nil {
		return i18n.New("没有安装系统服务：先执行 sudo velumd service install")
	}
	before, err := c.Status()
	if err != nil {
		return i18n.Errorf("连接 velumd 失败，升级前需要它在运行（sudo systemctl start velum）：%w", err)
	}
	if before.TUNTrial != nil {
		return i18n.New("TUN 正在试运行：先确认或关闭（sudo vlu tun confirm 或 off），再升级")
	}
	tmp, err := os.MkdirTemp("/var/tmp", "velum-upgrade-")
	if err != nil {
		return err
	}
	defer os.RemoveAll(tmp)

	target, pkg, sums, sig, err := fetchPackage(before, tmp, *file, *version, *beta)
	if err != nil || target == "" {
		return err
	}
	name := filepath.Base(pkg)
	if err := update.VerifySums(sums, sig, update.ReleasePublicKey, target); err != nil {
		return err
	}
	want, err := update.Checksum(sums, name)
	if err != nil {
		return err
	}
	if got, err := fileSHA256(pkg); err != nil {
		return err
	} else if got != want {
		return i18n.Errorf("%s 校验和不符：文件损坏或被改动", name)
	}
	i18n.Printf("校验通过：%s\n", name)
	newDir := filepath.Join(tmp, "new")
	if err := unpackBinaries(pkg, newDir); err != nil {
		return err
	}
	// 留一份当前的程序：velumd service install 需要 velumd 和 vlu 在同一目录
	oldDir := filepath.Join(tmp, "old")
	if err := os.Mkdir(oldDir, 0o700); err != nil {
		return err
	}
	for _, b := range []string{"velumd", "vlu"} {
		if err := copyExecutable(filepath.Join(filepath.Dir(installedVelumd), b), filepath.Join(oldDir, b)); err != nil {
			return i18n.Errorf("备份当前程序：%w", err)
		}
	}
	i18n.Printf("升级 %s → %s，服务会重启，连接会短暂中断\n", before.Version, target)
	if err := installFrom(newDir); err != nil {
		return rollback(oldDir, before.Version, err)
	}
	if err := waitHealthy(c, before, target); err != nil {
		return rollback(oldDir, before.Version, err)
	}
	i18n.Printf("已升级到 %s\n", target)
	return nil
}

func rollback(oldDir, old string, cause error) error {
	i18n.Fprintf(os.Stderr, "新版本没有正常运行：%v\n正在恢复 %s\n", cause, old)
	if err := installFrom(oldDir); err != nil {
		return i18n.Errorf("升级失败，恢复旧版本也失败：%w；查看 journalctl -u velum（升级失败的原因：%v）", err, cause)
	}
	return i18n.Errorf("升级失败，已恢复 %s：%w", old, cause)
}

func installFrom(dir string) error {
	out, err := exec.Command(filepath.Join(dir, "velumd"), "service", "install").CombinedOutput()
	if err != nil {
		return fmt.Errorf("%w：%s", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// waitHealthy 确认新版本确实在运行，而且没有丢掉升级前的状态：内核在运行的还在运行，TUN 开着的还开着。
func waitHealthy(c *rpc.Client, before *rpc.Status, target string) error {
	var last string
	for i := 0; i < 60; i++ {
		s, err := c.Status()
		switch {
		case err != nil:
			last = err.Error()
		case s.Version != target:
			last = i18n.T("运行的版本是 %s", s.Version)
		case s.Starting:
			last = i18n.T("仍在启动")
		case before.Running && !s.Running:
			last = i18n.T("内核没有运行：") + s.LastError
		case before.TUNOn && !s.TUNOn:
			last = i18n.T("TUN 没有恢复：") + s.LastError
		default:
			return nil
		}
		time.Sleep(500 * time.Millisecond)
	}
	return errors.New(last)
}

var pkgName = regexp.MustCompile(`^Velum-(.+)-linux-(x64|arm64)\.tar\.gz$`)

// fetchPackage 取得压缩包与校验文件。不需要升级时 target 为空。
func fetchPackage(before *rpc.Status, tmp, file, version string, beta bool) (target, pkg string, sums, sig []byte, err error) {
	suffix := update.AssetSuffix(runtime.GOOS, runtime.GOARCH)
	if file != "" {
		m := pkgName.FindStringSubmatch(filepath.Base(file))
		if m == nil {
			return "", "", nil, nil, i18n.Errorf("文件名应为 Velum-<版本>%s", suffix)
		}
		if !strings.HasSuffix(filepath.Base(file), suffix) {
			return "", "", nil, nil, i18n.Errorf("%s 不是这台机器的架构（%s）", filepath.Base(file), suffix)
		}
		dir := filepath.Dir(file)
		if sums, err = os.ReadFile(filepath.Join(dir, update.SumsName)); err == nil {
			sig, err = os.ReadFile(filepath.Join(dir, update.SigName))
		}
		if err != nil {
			return "", "", nil, nil, i18n.Errorf("本地升级需要同目录的 %s 和 %s：%w", update.SumsName, update.SigName, err)
		}
		return m[1], file, sums, sig, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Minute)
	defer cancel()
	for _, client := range upgradeClients(before.Listen) {
		var rel *update.Release
		if version != "" {
			rel, err = update.Find(ctx, client, update.ReleasesAPI, "velum/"+before.Version, version, suffix)
		} else {
			rel, err = update.Latest(ctx, client, update.ReleasesAPI, "velum/"+before.Version, beta || update.IsPrerelease(before.Version), suffix)
		}
		if err != nil {
			continue // 直连 GitHub 不通时经本机的 Velum 端口再试
		}
		switch {
		case rel == nil && version != "":
			return "", "", nil, nil, i18n.Errorf("没有版本 %s", version)
		case rel == nil:
			fmt.Println(i18n.T("没有可用的发布"))
			return "", "", nil, nil, nil
		case version == "" && !update.Newer(rel.Version, before.Version):
			i18n.Printf("已是最新版本（%s）\n", before.Version)
			return "", "", nil, nil, nil
		case rel.Download == "" || rel.Sums == "" || rel.Sig == "":
			return "", "", nil, nil, i18n.Errorf("版本 %s 没有可校验的 Linux 压缩包；请到 %s 手动下载", rel.Version, rel.URL)
		}
		pkg = filepath.Join(tmp, filepath.Base(rel.Download))
		i18n.Printf("下载 %s\n", filepath.Base(rel.Download))
		if err = download(ctx, client, rel.Download, pkg); err != nil {
			continue
		}
		if sums, err = fetchSmall(ctx, client, rel.Sums); err != nil {
			continue
		}
		if sig, err = fetchSmall(ctx, client, rel.Sig); err != nil {
			continue
		}
		return rel.Version, pkg, sums, sig, nil
	}
	return "", "", nil, nil, i18n.Errorf("下载失败（直连和经 Velum 端口都试过）：%w；也可以手动下载后用 --file 升级", err)
}

// upgradeClients：先按环境变量或直连，再经本机的 Velum 端口（有些服务器不能直连 GitHub）。
func upgradeClients(listen string) []*http.Client {
	mk := func(proxy func(*http.Request) (*url.URL, error)) *http.Client {
		tr := &http.Transport{Proxy: proxy, DialContext: (&net.Dialer{Timeout: 15 * time.Second}).DialContext,
			TLSHandshakeTimeout: 15 * time.Second, ResponseHeaderTimeout: 30 * time.Second}
		return &http.Client{Transport: tr}
	}
	out := []*http.Client{mk(http.ProxyFromEnvironment)}
	if listen != "" {
		out = append(out, mk(http.ProxyURL(&url.URL{Scheme: "http", Host: listen})))
	}
	return out
}

func download(ctx context.Context, c *http.Client, u, dst string) error {
	body, err := open(ctx, c, u)
	if err != nil {
		return err
	}
	defer body.Close()
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	defer f.Close()
	n, err := io.Copy(f, io.LimitReader(body, maxPackage+1))
	if err == nil && n > maxPackage {
		err = i18n.New("文件大得不正常")
	}
	return err
}

func fetchSmall(ctx context.Context, c *http.Client, u string) ([]byte, error) {
	body, err := open(ctx, c, u)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	return io.ReadAll(io.LimitReader(body, 1<<20))
}

func open(ctx context.Context, c *http.Client, u string) (io.ReadCloser, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, i18n.Errorf("%s 返回 %s", u, resp.Status)
	}
	return resp.Body, nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// unpackBinaries 只取出 velumd 和 vlu，忽略压缩包里的其他文件和目录结构，因此不受路径穿越影响。
func unpackBinaries(pkg, dir string) error {
	f, err := os.Open(pkg)
	if err != nil {
		return err
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		return err
	}
	if err := os.Mkdir(dir, 0o700); err != nil {
		return err
	}
	found := map[string]bool{}
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		base := filepath.Base(h.Name)
		if h.Typeflag != tar.TypeReg || (base != "velumd" && base != "vlu") {
			continue
		}
		out, err := os.OpenFile(filepath.Join(dir, base), os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
		if err != nil {
			return err
		}
		_, err = io.Copy(out, io.LimitReader(tr, maxPackage))
		if cerr := out.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return err
		}
		found[base] = true
	}
	if !found["velumd"] || !found["vlu"] {
		return i18n.New("压缩包里没有 velumd、vlu")
	}
	return nil
}

func copyExecutable(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	return out.Close()
}
