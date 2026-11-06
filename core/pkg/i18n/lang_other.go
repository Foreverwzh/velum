//go:build !windows && (!darwin || ios)

package i18n

// systemLanguage：其他平台按环境变量判断；移动端由 App 调用 SetLocale。
func systemLanguage() string { return "" }
