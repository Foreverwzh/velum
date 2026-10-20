package main

import (
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"
)

// 每种协议已映射的字段；出现未列出的字段即视为「降级：字段未映射」。
var commonProxyFields = []string{"name", "type", "server", "port", "udp", "tfo", "mptcp", "interface-name", "dialer-proxy", "ip-version"}
var tlsFields = []string{"tls", "sni", "servername", "skip-cert-verify", "alpn", "client-fingerprint", "reality-opts"}
var transportFields = []string{"network", "ws-opts", "grpc-opts", "h2-opts", "http-opts"}

var proxyFields = map[string][]string{
	"ss":        {"cipher", "password", "plugin", "plugin-opts", "udp-over-tcp"},
	"vmess":     append(append([]string{"uuid", "alterId", "cipher", "packet-encoding", "global-padding", "authenticated-length"}, tlsFields...), transportFields...),
	"vless":     append(append([]string{"uuid", "flow", "packet-encoding", "encryption"}, tlsFields...), transportFields...),
	"trojan":    append(append([]string{"password"}, tlsFields...), transportFields...),
	"anytls":    append([]string{"password", "idle-session-check-interval", "idle-session-timeout", "min-idle-session"}, tlsFields...),
	"hysteria2": append([]string{"password", "ports", "hop-interval", "up", "down", "obfs", "obfs-password"}, tlsFields...),
	"tuic":      append([]string{"uuid", "password", "congestion-controller", "udp-relay-mode", "reduce-rtt", "heartbeat-interval"}, tlsFields...),
	"socks5":    append([]string{"username", "password"}, tlsFields...),
	"http":      append([]string{"username", "password", "headers"}, tlsFields...),
	"wireguard": {"ip", "ipv6", "private-key", "public-key", "pre-shared-key", "allowed-ips", "reserved", "mtu", "persistent-keepalive"},
	"snell":     {"psk", "version", "obfs-opts"},
	"hysteria":  append([]string{"auth-str", "auth", "up", "down", "obfs", "protocol", "ports", "recv-window-conn", "recv-window"}, tlsFields...),
}

type convertedProxy struct {
	tag      string
	outbound obj   // 普通出站
	endpoint obj   // wireguard 等 endpoint
	extra    []obj // 辅助出站（如 shadow-tls 前置）
}

func convertProxy(p obj, fs *Findings) (*convertedProxy, bool) {
	typ := str(p, "type")
	name := str(p, "name")
	known, ok := proxyFields[typ]
	if !ok {
		fs.add("节点", typ, Unsupported, "sing-box 无对应协议或 spike 未实现", name)
		return nil, false
	}
	allowed := map[string]bool{}
	for _, k := range append(append([]string{}, commonProxyFields...), known...) {
		allowed[k] = true
	}
	var unmapped []string
	for _, k := range sortedKeys(p) {
		if !allowed[k] {
			unmapped = append(unmapped, k)
		}
	}

	port, _ := intv(p, "port")
	o := obj{"tag": name, "server": str(p, "server"), "server_port": port}
	var reasons []string
	if v, ok := boolv(p, "tfo"); ok && v {
		o["tcp_fast_open"] = true
	}
	if v, ok := boolv(p, "mptcp"); ok && v {
		o["tcp_multi_path"] = true
	}
	if v := str(p, "interface-name"); v != "" {
		o["bind_interface"] = v
	}
	if v := str(p, "dialer-proxy"); v != "" {
		o["detour"] = v
	}
	if v := str(p, "ip-version"); v != "" {
		if s, ok := ipVersionStrategy[v]; ok {
			o["domain_strategy"] = s
		} else {
			reasons = append(reasons, "ip-version="+v+" 无对应")
		}
	}
	var extra []obj
	udpOff := false
	if v, ok := boolv(p, "udp"); ok && !v {
		udpOff = true
	}

	switch typ {
	case "ss":
		o["type"] = "shadowsocks"
		o["method"] = str(p, "cipher")
		o["password"] = str(p, "password")
		if udpOff {
			o["network"] = "tcp"
		}
		if v, ok := boolv(p, "udp-over-tcp"); ok && v {
			o["udp_over_tcp"] = true
		}
		if pl := str(p, "plugin"); pl != "" {
			opts := sub(p, "plugin-opts")
			switch pl {
			case "obfs":
				o["plugin"] = "obfs-local"
				o["plugin_opts"] = fmt.Sprintf("obfs=%s;obfs-host=%s", str(opts, "mode"), str(opts, "host"))
			case "v2ray-plugin":
				o["plugin"] = "v2ray-plugin"
				s := "mode=" + str(opts, "mode")
				if b, _ := boolv(opts, "tls"); b {
					s += ";tls"
				}
				if h := str(opts, "host"); h != "" {
					s += ";host=" + h
				}
				if pa := str(opts, "path"); pa != "" {
					s += ";path=" + pa
				}
				o["plugin_opts"] = s
			case "shadow-tls":
				// sing-box：shadowtls 出站负责与服务器握手，ss 出站以它为 detour。
				stlsTag := name + "/shadow-tls"
				ver, _ := intv(opts, "version")
				if ver == 0 {
					ver = 2
				}
				stls := obj{"type": "shadowtls", "tag": stlsTag, "server": str(p, "server"), "server_port": port, "version": ver,
					"tls": obj{"enabled": true, "server_name": str(opts, "host")}}
				if pw := str(opts, "password"); pw != "" {
					stls["password"] = pw
				}
				if fp := str(p, "client-fingerprint"); fp != "" {
					stls["tls"].(obj)["utls"] = obj{"enabled": true, "fingerprint": fp}
				}
				o["detour"] = stlsTag
				delete(o, "server")
				delete(o, "server_port")
				for i, k := range unmapped {
					if k == "client-fingerprint" {
						unmapped = append(unmapped[:i], unmapped[i+1:]...)
						break
					}
				}
				extra = append(extra, stls)
			default:
				fs.add("节点", "ss+"+pl, Unsupported, "ss 插件 "+pl+" 无对应", name)
				return nil, false
			}
		}
	case "vmess":
		o["type"] = "vmess"
		o["uuid"] = str(p, "uuid")
		if n, ok := intv(p, "alterId"); ok {
			o["alter_id"] = n
		}
		if c := str(p, "cipher"); c != "" {
			o["security"] = c
		}
		if v := str(p, "packet-encoding"); v != "" {
			o["packet_encoding"] = v
		}
		if v, ok := boolv(p, "global-padding"); ok {
			o["global_padding"] = v
		}
		if v, ok := boolv(p, "authenticated-length"); ok {
			o["authenticated_length"] = v
		}
		if udpOff {
			o["network"] = "tcp"
		}
	case "vless":
		o["type"] = "vless"
		o["uuid"] = str(p, "uuid")
		if v := str(p, "flow"); v != "" {
			o["flow"] = v
		}
		if v := str(p, "packet-encoding"); v != "" {
			o["packet_encoding"] = v
		}
		if e := str(p, "encryption"); e != "" && e != "none" {
			fs.add("节点", "vless+encryption", Unsupported, "vless encryption="+e+" 无对应", name)
			return nil, false
		}
		if udpOff {
			o["network"] = "tcp"
		}
	case "trojan":
		o["type"] = "trojan"
		o["password"] = str(p, "password")
		if udpOff {
			o["network"] = "tcp"
		}
	case "anytls":
		o["type"] = "anytls"
		o["password"] = str(p, "password")
		if n, ok := intv(p, "idle-session-check-interval"); ok {
			o["idle_session_check_interval"] = fmt.Sprintf("%ds", n)
		}
		if n, ok := intv(p, "idle-session-timeout"); ok {
			o["idle_session_timeout"] = fmt.Sprintf("%ds", n)
		}
		if n, ok := intv(p, "min-idle-session"); ok {
			o["min_idle_session"] = n
		}
		if udpOff {
			reasons = append(reasons, "udp:false 无对应（anytls 不支持关闭 UDP）")
		}
	case "hysteria2":
		o["type"] = "hysteria2"
		o["password"] = str(p, "password")
		if ports := str(p, "ports"); ports != "" {
			o["server_ports"] = hy2Ports(ports)
		}
		if n, ok := intv(p, "hop-interval"); ok {
			o["hop_interval"] = fmt.Sprintf("%ds", n)
		}
		if n := mbps(str(p, "up")); n > 0 {
			o["up_mbps"] = n
		}
		if n := mbps(str(p, "down")); n > 0 {
			o["down_mbps"] = n
		}
		if ob := str(p, "obfs"); ob != "" {
			o["obfs"] = obj{"type": ob, "password": str(p, "obfs-password")}
		}
	case "tuic":
		o["type"] = "tuic"
		o["uuid"] = str(p, "uuid")
		o["password"] = str(p, "password")
		if v := str(p, "congestion-controller"); v != "" {
			o["congestion_control"] = v
		}
		if v := str(p, "udp-relay-mode"); v != "" {
			o["udp_relay_mode"] = v
		}
		if v, ok := boolv(p, "reduce-rtt"); ok {
			o["zero_rtt_handshake"] = v
		}
		if n, ok := intv(p, "heartbeat-interval"); ok {
			o["heartbeat"] = fmt.Sprintf("%dms", n)
		}
	case "snell":
		ver, _ := intv(p, "version")
		if ver != 4 && ver != 6 {
			fs.add("节点", fmt.Sprintf("snell v%d", ver), Unsupported, "sing-box 仅支持 snell v4 / v6", name)
			return nil, false
		}
		o["type"] = "snell"
		o["version"] = ver
		o["psk"] = str(p, "psk")
		if ob := sub(p, "obfs-opts"); ob != nil {
			o["obfs_mode"] = str(ob, "mode")
			o["obfs_host"] = str(ob, "host")
		}
	case "hysteria":
		o["type"] = "hysteria"
		if a := str(p, "auth-str"); a != "" {
			o["auth_str"] = a
		}
		if n := mbps(str(p, "up")); n > 0 {
			o["up_mbps"] = n
		}
		if n := mbps(str(p, "down")); n > 0 {
			o["down_mbps"] = n
		}
		if ob := str(p, "obfs"); ob != "" {
			o["obfs"] = ob
		}
		if pr := str(p, "protocol"); pr != "" && pr != "udp" {
			reasons = append(reasons, "protocol="+pr+"（faketcp / wechat-video）无对应")
		}
		if ports := str(p, "ports"); ports != "" {
			o["server_ports"] = hy2Ports(ports)
		}
	case "socks5":
		o["type"] = "socks"
		o["username"] = str(p, "username")
		o["password"] = str(p, "password")
		if udpOff {
			o["network"] = "tcp"
		}
	case "http":
		o["type"] = "http"
		o["username"] = str(p, "username")
		o["password"] = str(p, "password")
	case "wireguard":
		ep, rs := convertWireGuard(p, name)
		reasons = append(reasons, rs...)
		for _, k := range unmapped {
			reasons = append(reasons, "字段 "+k+" 未映射")
		}
		record(fs, typ, name, reasons)
		return &convertedProxy{tag: name, endpoint: ep}, true
	}

	if tlsNeeded(typ, p) {
		t, rs := convertTLS(typ, p)
		reasons = append(reasons, rs...)
		o["tls"] = t
	}
	if hasTransport(typ) {
		t, rs := convertTransport(p)
		reasons = append(reasons, rs...)
		if t != nil {
			o["transport"] = t
		}
	}
	for _, k := range unmapped {
		reasons = append(reasons, "字段 "+k+" 未映射")
	}
	record(fs, typ, name, reasons)
	return &convertedProxy{tag: name, outbound: o, extra: extra}, true
}

func record(fs *Findings, typ, name string, reasons []string) {
	if len(reasons) == 0 {
		fs.add("节点", typ, Supported, "", name)
		return
	}
	for _, r := range reasons {
		fs.add("节点", typ, Degraded, r, name)
	}
}

var ipVersionStrategy = map[string]string{
	"dual": "", "ipv4": "ipv4_only", "ipv6": "ipv6_only", "ipv4-prefer": "prefer_ipv4", "ipv6-prefer": "prefer_ipv6",
}

func hasTransport(typ string) bool { return typ == "vmess" || typ == "vless" || typ == "trojan" }

func tlsNeeded(typ string, p obj) bool {
	switch typ {
	case "trojan", "anytls", "hysteria", "hysteria2", "tuic":
		return true
	}
	b, _ := boolv(p, "tls")
	return b || sub(p, "reality-opts") != nil
}

func convertTLS(typ string, p obj) (obj, []string) {
	t := obj{"enabled": true}
	var reasons []string
	if sni := str(p, "sni", "servername"); sni != "" {
		t["server_name"] = sni
	}
	if b, _ := boolv(p, "skip-cert-verify"); b {
		t["insecure"] = true
	}
	if alpn := strList(p["alpn"]); len(alpn) > 0 {
		t["alpn"] = alpn
	}
	fp := str(p, "client-fingerprint")
	if r := sub(p, "reality-opts"); r != nil {
		t["reality"] = obj{"enabled": true, "public_key": str(r, "public-key"), "short_id": str(r, "short-id")}
		if fp == "" {
			fp = "chrome" // reality 必须启用 uTLS
			reasons = append(reasons, "reality 未指定指纹，默认 chrome")
		}
	}
	if fp != "" {
		t["utls"] = obj{"enabled": true, "fingerprint": fp}
	}
	return t, reasons
}

func convertTransport(p obj) (obj, []string) {
	switch n := str(p, "network"); n {
	case "", "tcp":
		return nil, nil
	case "ws":
		o := sub(p, "ws-opts")
		t := obj{"type": "ws", "path": str(o, "path")}
		if h := sub(o, "headers"); h != nil {
			t["headers"] = h
		}
		if n, ok := intv(o, "max-early-data"); ok {
			t["max_early_data"] = n
			t["early_data_header_name"] = str(o, "early-data-header-name")
		}
		return t, nil
	case "grpc":
		return obj{"type": "grpc", "service_name": str(sub(p, "grpc-opts"), "grpc-service-name")}, nil
	case "h2":
		o := sub(p, "h2-opts")
		return obj{"type": "http", "host": strList(o["host"]), "path": str(o, "path")}, nil
	case "http":
		o := sub(p, "http-opts")
		t := obj{"type": "http", "method": str(o, "method")}
		if ps := strList(o["path"]); len(ps) > 0 {
			t["path"] = ps[0]
		}
		return t, []string{"http-opts 仅映射首个 path"}
	default:
		return nil, []string{"network=" + n + " 无对应"}
	}
}

func convertWireGuard(p obj, name string) (obj, []string) {
	var reasons []string
	var addrs []string
	if ip := str(p, "ip"); ip != "" {
		addrs = append(addrs, hostPrefix(ip))
	}
	if ip := str(p, "ipv6"); ip != "" {
		addrs = append(addrs, hostPrefix(ip))
	}
	port, _ := intv(p, "port")
	peer := obj{"address": str(p, "server"), "port": port, "public_key": str(p, "public-key")}
	if v := str(p, "pre-shared-key"); v != "" {
		peer["pre_shared_key"] = v
	}
	if ips := strList(p["allowed-ips"]); len(ips) > 0 {
		peer["allowed_ips"] = ips
	} else {
		peer["allowed_ips"] = []string{"0.0.0.0/0", "::/0"}
	}
	if r, ok := p["reserved"]; ok {
		peer["reserved"] = r
	}
	if n, ok := intv(p, "persistent-keepalive"); ok {
		peer["persistent_keepalive_interval"] = n
	}
	ep := obj{"type": "wireguard", "tag": name, "address": addrs, "private_key": str(p, "private-key"), "peers": []obj{peer}}
	if n, ok := intv(p, "mtu"); ok {
		ep["mtu"] = n
	}
	// 映射为 sing-box endpoint（非 outbound），语义等价；需 with_wireguard 构建标签。
	return ep, reasons
}

func hostPrefix(s string) string {
	if strings.Contains(s, "/") {
		return s
	}
	a, err := netip.ParseAddr(s)
	if err != nil {
		return s
	}
	return netip.PrefixFrom(a, a.BitLen()).String()
}

var mbpsRe = regexp.MustCompile(`^\s*(\d+)\s*([a-zA-Z]*)`)

func mbps(s string) int {
	m := mbpsRe.FindStringSubmatch(s)
	if m == nil {
		return 0
	}
	n, _ := strconv.Atoi(m[1])
	switch strings.ToLower(m[2]) {
	case "gbps", "g":
		return n * 1000
	case "kbps", "k":
		return n / 1000
	}
	return n
}

func hy2Ports(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if !strings.Contains(part, "-") {
			part = part + "-" + part // sing-box 要求 server_ports 均为区间
		}
		out = append(out, strings.ReplaceAll(part, "-", ":"))
	}
	return out
}
