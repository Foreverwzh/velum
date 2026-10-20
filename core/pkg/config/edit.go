package config

import (
	"bytes"
	"fmt"
	"slices"
	"strings"

	"gopkg.in/yaml.v3"
)

// SetFields 修改 config.yaml 中 section 下的若干标量字段，返回新的文件内容（不写盘）。
//
// 只替换字段所在的那一行（保留缩进与行尾注释），其余内容逐字节保留；
// 字段不存在时插入到该节开头，节不存在时追加到文件末尾。
func SetFields(data []byte, section string, fields map[string]string, order []string) ([]byte, error) {
	vals := map[string]value{}
	for k, v := range fields {
		vals[k] = value{text: scalar(v)}
	}
	return setFields(data, section, vals, order)
}

// SetLists 修改 section 下的若干字符串列表字段（写为单行 [a, b]）。原有的多行写法（- a）会被改写为单行，
// 其间的注释不保留。
func SetLists(data []byte, section string, lists map[string][]string, order []string) ([]byte, error) {
	vals := map[string]value{}
	for k, vs := range lists {
		items := make([]string, len(vs))
		for i, v := range vs {
			items[i] = scalar(v)
		}
		vals[k] = value{text: "[" + strings.Join(items, ", ") + "]", list: true}
	}
	return setFields(data, section, vals, order)
}

type value struct {
	text string // 已编码的 YAML
	list bool
}

func setFields(data []byte, section string, fields map[string]value, order []string) ([]byte, error) {
	var doc yaml.Node
	if len(bytes.TrimSpace(data)) > 0 {
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return nil, fmt.Errorf("解析 config.yaml：%w", err)
		}
	}
	var root *yaml.Node
	if doc.Kind == yaml.DocumentNode && len(doc.Content) > 0 {
		root = doc.Content[0]
		if root.Kind != yaml.MappingNode {
			return nil, fmt.Errorf("config.yaml 顶层不是映射")
		}
	}
	lines := strings.Split(string(data), "\n")
	if n := len(lines); n > 0 && lines[n-1] == "" {
		lines = lines[:n-1] // 末尾换行，最后统一补回
	}

	var secKey, sec *yaml.Node
	if root != nil {
		secKey, sec = lookup(root, section)
	}
	if secKey == nil {
		lines = append(lines, section+":")
		for _, k := range order {
			if v, ok := fields[k]; ok {
				lines = append(lines, "  "+k+": "+v.text)
			}
		}
		return []byte(strings.Join(lines, "\n") + "\n"), nil
	}
	if sec.Kind != yaml.MappingNode && !(sec.Kind == yaml.ScalarNode && sec.Tag == "!!null") {
		return nil, fmt.Errorf("config.yaml 中 %s 不是映射", section)
	}

	indent := secKey.Column - 1 + 2
	var inserts []string
	for _, k := range order {
		v, ok := fields[k]
		if !ok {
			continue
		}
		var kn, vn *yaml.Node
		if sec.Kind == yaml.MappingNode && sec.Style&yaml.FlowStyle == 0 {
			kn, vn = lookup(sec, k)
		}
		if kn == nil {
			inserts = append(inserts, k+": "+v.text)
			continue
		}
		last := kn.Line // 值占用的最后一行
		switch {
		case !v.list && vn.Kind == yaml.ScalarNode && vn.Style&(yaml.LiteralStyle|yaml.FoldedStyle) == 0 && vn.Line == kn.Line:
		case v.list && vn.Kind == yaml.ScalarNode && vn.Tag == "!!null" && vn.Line == kn.Line: // `key:`（空）
		case v.list && vn.Kind == yaml.SequenceNode && flatScalars(vn):
			if vn.Style&yaml.FlowStyle != 0 {
				if n := len(vn.Content); n > 0 && vn.Content[n-1].Line != kn.Line {
					return nil, fmt.Errorf("config.yaml 中 %s.%s 跨多行书写，请手动编辑", section, k)
				}
			} else if n := len(vn.Content); n > 0 {
				last = vn.Content[n-1].Line
			}
		default:
			return nil, fmt.Errorf("config.yaml 中 %s.%s 的写法无法自动修改，请手动编辑", section, k)
		}
		indent = kn.Column - 1
		line := strings.Repeat(" ", kn.Column-1) + k + ": " + v.text
		if c := vn.LineComment + kn.LineComment; c != "" { // 多行列表 / 空值时注释挂在键上
			line += " " + c
		}
		lines[kn.Line-1] = line
		for i := kn.Line; i < last; i++ {
			lines[i] = "\x00" // 原多行列表的其余行，最后统一删除
		}
	}
	lines = slices.DeleteFunc(lines, func(l string) bool { return l == "\x00" })
	if len(inserts) > 0 {
		at := secKey.Line // 节标题的下一行
		if sec.Kind != yaml.MappingNode || sec.Style&yaml.FlowStyle != 0 {
			if len(sec.Content) > 0 {
				return nil, fmt.Errorf("config.yaml 中 %s 使用了单行写法，请手动编辑", section)
			}
			// `section:` / `section: {}` / `section: null`：改写标题行（保留行尾注释）
			head := strings.Repeat(" ", secKey.Column-1) + section + ":"
			if c := sec.LineComment + secKey.LineComment; c != "" {
				head += " " + c
			}
			lines[secKey.Line-1] = head
		} else if len(sec.Content) > 0 {
			indent = sec.Content[0].Column - 1
		}
		pad := strings.Repeat(" ", indent)
		for i := range inserts {
			inserts[i] = pad + inserts[i]
		}
		lines = append(lines[:at], append(inserts, lines[at:]...)...)
	}
	return []byte(strings.Join(lines, "\n") + "\n"), nil
}

// flatScalars：序列的元素都是单行标量。
func flatScalars(n *yaml.Node) bool {
	for _, c := range n.Content {
		if c.Kind != yaml.ScalarNode || c.Style&(yaml.LiteralStyle|yaml.FoldedStyle) != 0 {
			return false
		}
	}
	return true
}

func lookup(m *yaml.Node, key string) (k, v *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i], m.Content[i+1]
		}
	}
	return nil, nil
}

// scalar 把字符串编码为单行 YAML 标量（必要时加引号）。
func scalar(v string) string {
	if v == "" {
		return `""`
	}
	out, err := yaml.Marshal(v)
	if err != nil || bytes.Count(bytes.TrimRight(out, "\n"), []byte("\n")) > 0 {
		return fmt.Sprintf("%q", v)
	}
	return strings.TrimRight(string(out), "\n")
}
