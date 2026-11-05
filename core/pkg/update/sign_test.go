package update

import (
	"encoding/base64"
	"encoding/json"
	"os"
	"strings"
	"testing"
)

// testdata 是用一次性密钥、由 tauri signer 真实生成的（tauri signer sign --app-version 1.2.3），不是这个包自己算的：
// 这样能发现和发布流程实际使用的工具不兼容。
func vector(t *testing.T) (sums, sig []byte, pub string) {
	t.Helper()
	read := func(n string) []byte {
		b, err := os.ReadFile("testdata/" + n)
		if err != nil {
			t.Fatal(err)
		}
		return b
	}
	return read("SHA256SUMS"), read("SHA256SUMS.sig"), strings.TrimSpace(string(read("test.pub")))
}

func TestVerifySums(t *testing.T) {
	sums, sig, pub := vector(t)
	if err := VerifySums(sums, sig, pub, "1.2.3"); err != nil {
		t.Fatalf("真实签名应通过：%v", err)
	}
	if err := VerifySums(append([]byte("x"), sums...), sig, pub, "1.2.3"); err == nil {
		t.Error("文件被改动应失败")
	}
	if err := VerifySums(sums, sig, pub, "1.2.4"); err == nil || !strings.Contains(err.Error(), "版本") {
		t.Errorf("旧版本的签名不能冒充新版本：%v", err)
	}
	if err := VerifySums(sums, sig, ReleasePublicKey, "1.2.3"); err == nil {
		t.Error("其他密钥的签名应失败")
	}
	if err := VerifySums(sums, []byte("not base64!"), pub, "1.2.3"); err == nil {
		t.Error("格式错误应失败")
	}
	// 改动受保护的注释（把版本改成新的）：全局签名应失败
	text, _ := base64.StdEncoding.DecodeString(string(sig))
	forged := strings.Replace(string(text), "version:1.2.3", "version:9.9.9", 1)
	if err := VerifySums(sums, []byte(base64.StdEncoding.EncodeToString([]byte(forged))), pub, "9.9.9"); err == nil {
		t.Error("伪造版本号应失败")
	}
}

// 公钥必须和桌面端一致，否则发布流程只有一把私钥，其中一边会校验不过。
func TestReleaseKeyMatchesDesktop(t *testing.T) {
	b, err := os.ReadFile("../../../desktop/src-tauri/tauri.conf.json")
	if err != nil {
		t.Skip("没有桌面端源码：", err)
	}
	var c struct {
		Plugins struct {
			Updater struct {
				Pubkey string `json:"pubkey"`
			} `json:"updater"`
		} `json:"plugins"`
	}
	if err := json.Unmarshal(b, &c); err != nil {
		t.Fatal(err)
	}
	if c.Plugins.Updater.Pubkey != ReleasePublicKey {
		t.Fatal("ReleasePublicKey 与 tauri.conf.json 不一致")
	}
}

func TestChecksum(t *testing.T) {
	sums := []byte("aaa  Velum-1-linux-x64.tar.gz\nbbb *velum-install.sh\n")
	if v, err := Checksum(sums, "velum-install.sh"); err != nil || v != "bbb" {
		t.Errorf("%q %v", v, err)
	}
	if _, err := Checksum(sums, "other"); err == nil {
		t.Error("没有的文件应报错")
	}
}
