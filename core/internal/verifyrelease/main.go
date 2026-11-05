// verifyrelease 在发布前校验 SHA256SUMS 的签名：私钥和程序内置的公钥不匹配时，让发布失败，而不是发出一个无法升级的版本。
// 用法：go run ./internal/verifyrelease <SHA256SUMS> <SHA256SUMS.sig> <版本>
package main

import (
	"fmt"
	"os"

	"github.com/Foreverwzh/velum/core/pkg/update"
)

func main() {
	if len(os.Args) != 4 {
		fmt.Fprintln(os.Stderr, "用法：verifyrelease <SHA256SUMS> <SHA256SUMS.sig> <版本>")
		os.Exit(2)
	}
	sums, err := os.ReadFile(os.Args[1])
	if err == nil {
		var sig []byte
		if sig, err = os.ReadFile(os.Args[2]); err == nil {
			err = update.VerifySums(sums, sig, update.ReleasePublicKey, os.Args[3])
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "verifyrelease:", err)
		os.Exit(1)
	}
	fmt.Println("签名校验通过：", os.Args[1])
}
