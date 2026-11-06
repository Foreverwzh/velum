package update

import (
	"bytes"
	"crypto/ed25519"
	"encoding/base64"
	"github.com/Foreverwzh/velum/core/pkg/i18n"
	"strings"

	"golang.org/x/crypto/blake2b"
)

// ReleasePublicKey 是发布签名的公钥，与桌面端 updater 是同一把（desktop/src-tauri/tauri.conf.json）。
// 测试时可经 -ldflags -X 替换。
var ReleasePublicKey = "dW50cnVzdGVkIGNvbW1lbnQ6IG1pbmlzaWduIHB1YmxpYyBrZXk6IDQ0NDJDMjE1NTQxNkE0NTAKUldSUXBCWlVGY0pDUkJIbEk0SWpvNmI4VWgwTFMyQXZOaUpOZGdrazcyTzdMdE1NOUUxUlZEOWkK"

// SumsName 是校验和文件名，SigName 是它的签名（由 .github/workflows/release.yml 生成）。
const (
	SumsName = "SHA256SUMS"
	SigName  = "SHA256SUMS.sig"
)

// VerifySums 校验 SHA256SUMS 的签名（tauri signer 产生的 minisign 格式），并确认签名绑定了指定的版本：
// 版本写在受签名保护的注释里，所以旧版本的合法签名不能被用来冒充新版本。
func VerifySums(sums, sig []byte, pubkey, version string) error {
	keyID, pub, err := parsePublicKey(pubkey)
	if err != nil {
		return i18n.Errorf("发布公钥无效：%w", err)
	}
	text, err := base64.StdEncoding.DecodeString(strings.TrimSpace(string(sig)))
	if err != nil {
		return i18n.Errorf("签名格式错误：%w", err)
	}
	lines := strings.Split(strings.TrimRight(string(text), "\n"), "\n")
	if len(lines) != 4 {
		return i18n.New("签名格式错误：应为 4 行")
	}
	body, err := base64.StdEncoding.DecodeString(lines[1])
	if err != nil || len(body) != 74 {
		return i18n.New("签名格式错误：签名内容无效")
	}
	// 只接受预哈希算法（ED）；旧的 Ed 没有理由出现，接受它只会增加攻击面
	if string(body[:2]) != "ED" {
		return i18n.Errorf("不支持的签名算法 %q", body[:2])
	}
	if !bytes.Equal(body[2:10], keyID) {
		return i18n.New("签名不是由 Velum 的发布密钥产生的")
	}
	digest := blake2b.Sum512(sums)
	if !ed25519.Verify(pub, digest[:], body[10:]) {
		return i18n.New("签名校验失败：文件被改动过，或不是由 Velum 发布的")
	}
	comment, ok := strings.CutPrefix(lines[2], "trusted comment: ")
	if !ok {
		return i18n.New("签名格式错误：缺少受保护的注释")
	}
	global, err := base64.StdEncoding.DecodeString(lines[3])
	if err != nil || len(global) != ed25519.SignatureSize {
		return i18n.New("签名格式错误：全局签名无效")
	}
	if !ed25519.Verify(pub, append(append([]byte(nil), body[10:]...), comment...), global) {
		return i18n.New("签名校验失败：受保护的注释被改动过")
	}
	fields := map[string]string{}
	for _, f := range strings.Split(comment, "\t") {
		if k, v, ok := strings.Cut(f, ":"); ok {
			fields[k] = v
		}
	}
	if fields["file"] != SumsName {
		return i18n.Errorf("签名针对的是 %q，不是 %s", fields["file"], SumsName)
	}
	if fields["version"] != version {
		return i18n.Errorf("签名针对的版本是 %q，不是 %s", fields["version"], version)
	}
	return nil
}

func parsePublicKey(b64 string) (keyID []byte, pub ed25519.PublicKey, err error) {
	text, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		return nil, nil, err
	}
	lines := strings.Split(strings.TrimSpace(string(text)), "\n")
	if len(lines) != 2 {
		return nil, nil, i18n.New("应为 2 行")
	}
	raw, err := base64.StdEncoding.DecodeString(lines[1])
	if err != nil {
		return nil, nil, err
	}
	if len(raw) != 42 || string(raw[:2]) != "Ed" {
		return nil, nil, i18n.New("不是 Ed25519 公钥")
	}
	return raw[2:10], ed25519.PublicKey(raw[10:]), nil
}

// Checksum 返回 SHA256SUMS 中某个文件的校验和。
func Checksum(sums []byte, name string) (string, error) {
	for _, line := range strings.Split(string(sums), "\n") {
		f := strings.Fields(line)
		if len(f) == 2 && strings.TrimPrefix(f[1], "*") == name {
			return f[0], nil
		}
	}
	return "", i18n.Errorf("%s 里没有 %s", SumsName, name)
}
