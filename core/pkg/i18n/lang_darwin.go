//go:build darwin && !ios

package i18n

import (
	"os/exec"
	"strings"
)

// systemLanguage 读 macOS 的首选语言：终端的 LANG 经常是 en_US，和用户的系统语言不一致。
func systemLanguage() string {
	out, err := exec.Command("/usr/bin/defaults", "read", "-g", "AppleLanguages").Output()
	if err != nil {
		return ""
	}
	for _, line := range strings.Split(string(out), "\n") {
		if s := strings.Trim(strings.TrimSpace(line), `",`); s != "" && s != "(" && s != ")" {
			return s
		}
	}
	return ""
}
