package main

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
)

type obj = map[string]any

// Status 是单个配置项的转换结论。
type Status string

const (
	Supported   Status = "支持"
	Degraded    Status = "降级"
	Unsupported Status = "不支持"
)

// Finding 是一条转换结论。Kind 只包含类型 / 字段名等非敏感信息；
// Detail 可能包含名称，只写入本地私有报告。
type Finding struct {
	Category string // 节点 / 策略组 / 规则 / 规则集 / DNS / 全局
	Kind     string // 例如 "ss"、"url-test"、"DOMAIN-WILDCARD"
	Status   Status
	Reason   string // 非敏感的原因描述
	Detail   string // 私有：具体名称
}

type Findings struct{ list []Finding }

func (f *Findings) add(cat, kind string, st Status, reason, detail string) {
	f.list = append(f.list, Finding{cat, kind, st, reason, detail})
}

// --- yaml map helpers ---

func str(m obj, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok && v != nil {
			switch t := v.(type) {
			case string:
				return t
			default:
				return fmt.Sprint(t)
			}
		}
	}
	return ""
}

func intv(m obj, k string) (int, bool) {
	v, ok := m[k]
	if !ok || v == nil {
		return 0, false
	}
	switch t := v.(type) {
	case int:
		return t, true
	case int64:
		return int(t), true
	case float64:
		return int(t), true
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(t))
		return n, err == nil
	}
	return 0, false
}

func boolv(m obj, k string) (bool, bool) {
	v, ok := m[k]
	if !ok || v == nil {
		return false, false
	}
	switch t := v.(type) {
	case bool:
		return t, true
	case string:
		b, err := strconv.ParseBool(t)
		return b, err == nil
	}
	return false, false
}

func sub(m obj, k string) obj {
	if v, ok := m[k].(map[string]any); ok {
		return v
	}
	return nil
}

func strList(v any) []string {
	switch t := v.(type) {
	case []any:
		out := make([]string, 0, len(t))
		for _, x := range t {
			out = append(out, fmt.Sprint(x))
		}
		return out
	case []string:
		return t
	case string:
		if t == "" {
			return nil
		}
		return []string{t}
	}
	return nil
}

func sortedKeys[M ~map[string]V, V any](m M) []string {
	ks := make([]string, 0, len(m))
	for k := range m {
		ks = append(ks, k)
	}
	sort.Strings(ks)
	return ks
}
