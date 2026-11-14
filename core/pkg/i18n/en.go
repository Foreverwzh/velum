// 中文源文字 → 英文，数据在 en.json：Rust 核心（iOS、Android、macOS、Linux）用同一份，见 core/scripts/core-i18n.sh。
// 由 catalog_test.go 保证完整，也不留已经不用的条目。
package i18n

import (
	_ "embed"
	"encoding/json"
)

//go:embed en.json
var enJSON []byte

var en = func() map[string]string {
	m := map[string]string{}
	if err := json.Unmarshal(enJSON, &m); err != nil {
		panic("i18n: en.json: " + err.Error())
	}
	return m
}()
