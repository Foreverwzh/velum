package i18n

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

var han = regexp.MustCompile(`\p{Han}`)
var verb = regexp.MustCompile(`%[-+# 0-9.]*[a-zA-Z%]`)

func isLogCall(c *ast.CallExpr) bool {
	sel, ok := c.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	switch sel.Sel.Name {
	case "Printf", "Println", "Print", "Fatalf", "Fatal", "Panicf", "Fatalln":
	default:
		return false
	}
	switch x := sel.X.(type) {
	case *ast.Ident:
		n := strings.ToLower(x.Name)
		return n == "log" || n == "logger" || n == "l"
	case *ast.SelectorExpr:
		return strings.Contains(strings.ToLower(x.Sel.Name), "log")
	}
	return false
}

// sources 扫描 core 下面向用户的中文字面量：测试和日志除外。
func sources(t *testing.T) map[string]string {
	t.Helper()
	out := map[string]string{}
	root := filepath.Join("..", "..")
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if n := d.Name(); n == "third_party" || n == "internal" || n == "i18n" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		fset := token.NewFileSet()
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		var stack []ast.Node
		ast.Inspect(f, func(n ast.Node) bool {
			if n == nil {
				stack = stack[:len(stack)-1]
				return true
			}
			defer func() { stack = append(stack, n) }()
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING || !han.MatchString(lit.Value) {
				return true
			}
			if len(stack) > 0 {
				switch par := stack[len(stack)-1].(type) {
				case *ast.CallExpr:
					if isLogCall(par) {
						return true
					}
				case *ast.Field:
					return true
				}
			}
			s, _ := strconv.Unquote(lit.Value)
			out[s] = fset.Position(lit.Pos()).String()
			return true
		})
		return nil
	})
	return out
}

func TestEveryStringIsTranslated(t *testing.T) {
	for s, at := range sources(t) {
		if _, ok := en[s]; !ok {
			t.Errorf("%s 缺少英文：%q", at, s)
		}
	}
}

func TestNoStaleTranslations(t *testing.T) {
	src := sources(t)
	for s := range en {
		if _, ok := src[s]; !ok {
			t.Errorf("英文对照里有已经不用的条目：%q", s)
		}
	}
}

// 占位符写错会在运行时变成 %!s(MISSING) 之类的乱码。
func TestTranslationsKeepVerbs(t *testing.T) {
	for s, e := range en {
		a, b := verb.FindAllString(s, -1), verb.FindAllString(e, -1)
		if strings.Join(a, " ") != strings.Join(b, " ") {
			t.Errorf("占位符不一致：%q → %q", s, e)
		}
		if han.MatchString(e) {
			t.Errorf("英文里还有中文：%q", e)
		}
	}
}

func TestLocale(t *testing.T) {
	defer SetLocale(Zh)
	for in, want := range map[string]string{"zh-Hans-CN": Zh, "zh_CN.UTF-8": Zh, "ZH": Zh, "en-US": En, "fr": En, "": En} {
		if got := Normalize(in); got != want {
			t.Errorf("Normalize(%q) = %q，应为 %q", in, got, want)
		}
	}
	en["测试%s"] = "test %s"
	defer delete(en, "测试%s")
	SetLocale(En)
	if got := T("测试%s", "x"); got != "test x" {
		t.Errorf("T = %q", got)
	}
	if got := T("没有译文"); got != "没有译文" {
		t.Errorf("没有译文时应当显示原文：%q", got)
	}
	if got := Errorf("测试%s", "y").Error(); got != "test y" {
		t.Errorf("Errorf = %q", got)
	}
	e := Static("测试%s")
	if e.Error() != "test %s" {
		t.Errorf("Static 应当在读取时翻译：%q", e.Error())
	}
	SetLocale(Zh)
	if e.Error() != "测试%s" {
		t.Errorf("Static 应当跟随语言：%q", e.Error())
	}
}
