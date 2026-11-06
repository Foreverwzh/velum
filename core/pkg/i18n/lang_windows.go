//go:build windows

package i18n

import "golang.org/x/sys/windows"

// systemLanguage 读 Windows 的界面语言。
func systemLanguage() string {
	langs, err := windows.GetUserPreferredUILanguages(windows.MUI_LANGUAGE_NAME)
	if err != nil || len(langs) == 0 {
		return ""
	}
	return langs[0]
}
