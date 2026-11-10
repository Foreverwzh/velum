// i18ndump 把英文表（pkg/i18n/en.go）导出为 JSON（中文源文字 → 英文），给 iOS 的 Rust 核心用：
//
//	go run ./internal/i18ndump > en.json
package main

import (
	"encoding/json"
	"os"

	"github.com/Foreverwzh/velum/core/pkg/i18n"
)

func main() {
	enc := json.NewEncoder(os.Stdout)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", " ")
	if err := enc.Encode(i18n.English()); err != nil {
		os.Exit(1)
	}
}
