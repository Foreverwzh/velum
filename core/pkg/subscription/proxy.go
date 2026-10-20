package subscription

import (
	"fmt"
	"net/netip"
	"regexp"
	"strconv"
	"strings"

	"github.com/Foreverwzh/velum/core/pkg/ir"
)

var commonProxyFields = []string{"name", "type", "server", "port", "udp", "tfo", "mptcp", "interface-name", "dialer-proxy", "ip-version"}
var tlsFields = []string{"tls", "sni", "servername", "skip-cert-verify", "alpn", "client-fingerprint", "reality-opts"}
var transportFields = []string{"network", "ws-opts", "grpc-opts", "h2-opts", "http-opts"}

func join(parts ...[]string) []string {
	var out []string
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}

// proxyFields 列出每种协议已映射的字段；其余字段一律产生「字段未映射」告警。
var proxyFields = map[string][]string{
	"ss":        {"cipher", "password", "plugin", "plugin-opts", "udp-over-tcp", "client-fingerprint"},
	"vmess":     join([]string{"uuid", "alterId", "cipher", "packet-encoding", "global-padding", "authenticated-length"}, tlsFields, transportFields),
	"vless":     join([]string{"uuid", "flow", "packet-encoding", "encryption"}, tlsFields, transportFields),
	"trojan":    join([]string{"password"}, tlsFields, transportFields),
	"anytls":    join([]string{"password", "idle-session-check-interval", "idle-session-timeout", "min-idle-session"}, tlsFields),
	"hysteria2": join([]string{"password", "ports", "hop-interval", "up", "down", "obfs", "obfs-password"}, tlsFields),
	"hysteria":  join([]string{"auth-str", "auth", "up", "down", "obfs", "protocol", "ports", "recv-window-conn", "recv-window"}, tlsFields),
	"tuic":      join([]string{"uuid", "password", "congestion-controller", "udp-relay-mode", "reduce-rtt", "heartbeat-interval"}, tlsFields),
	"socks5":    join([]string{"username", "password"}, tlsFields),
	"http":      join([]string{"username", "password", "headers"}, tlsFields),
	"snell":     {"psk", "version", "obfs-opts"},
	"wireguard": {"ip", "ipv6", "private-key", "public-key", "pre-shared-key", "allowed-ips", "reserved", "mtu", "persistent-keepalive"},
}

var protocolName = map[string]string{
	"ss": "shadowsocks", "vmess": "vmess", "vless": "vless", "trojan": "trojan", "anytls": "anytls",
	"hysteria2": "hysteria2", "hysteria": "hysteria", "tuic": "tuic", "socks5": "socks", "http": "http",
	"snell": "snell", "wireguard": "wireguard",
}

var ipVersionStrategy = map[string]string{
	"dual": "", "ipv4": "ipv4_only", "ipv6": "ipv6_only", "ipv4-prefer": "prefer_ipv4", "ipv6-prefer": "prefer_ipv6",
}

// convertProxy 返回主节点及其辅助节点（如 shadow-tls 前置）。ok=false 表示节点不可用（已产生告警）。
func (c *converter) convertProxy(p obj, src ir.Source) (main ir.Node, extra []ir.Node, ok bool) {
	typ := str(p, "type")
	name := str(p, "name")
	src.Text = "proxy " + name + " (" + typ + ")"
	known, supported := proxyFields[typ]
	if !supported {
		c.warn("node", typ, ir.Unsupported, "协议无对应", name, src)
		return main, nil, false
	}
	allowed := map[string]bool{}
	for _, k := range join(commonProxyFields, known) {
		allowed[k] = true
	}
	var unmapped []string
	for _, k := range sortedKeys(p) {
		if !allowed[k] {
			unmapped = append(unmapped, k)
		}
	}
	port, _ := intv(p, "port")
	n := ir.Node{Tag: name, Protocol: protocolName[typ], Server: str(p, "server"), Port: port, Options: map[string]any{}, Source: src}
	var reasons []string
	if v, ok := boolv(p, "tfo"); ok && v {
		n.Dial.TCPFastOpen = true
	}
	if v, ok := boolv(p, "mptcp"); ok && v {
		n.Dial.MPTCP = true
	}
	n.Dial.BindInterface = str(p, "interface-name")
	n.Detour = str(p, "dialer-proxy")
	if v := str(p, "ip-version"); v != "" {
		if s, ok := ipVersionStrategy[v]; ok {
			n.Dial.DomainStrategy = s
		} else {
			reasons = append(reasons, "ip-version="+v+" 无对应")
		}
	}
	udpOff := false
	if v, ok := boolv(p, "udp"); ok && !v {
		udpOff = true
	}
	tcpOnly := func() {
		if udpOff {
			n.Options["network"] = "tcp"
		}
	}

	switch typ {
	case "ss":
		n.Options["method"] = str(p, "cipher")
		n.Options["password"] = str(p, "password")
		tcpOnly()
		if v, ok := boolv(p, "udp-over-tcp"); ok && v {
			n.Options["udp_over_tcp"] = true
		}
		fpUsed := false
		if pl := str(p, "plugin"); pl != "" {
			opts := sub(p, "plugin-opts")
			switch pl {
			case "obfs":
				n.Options["plugin"] = "obfs-local"
				n.Options["plugin_opts"] = fmt.Sprintf("obfs=%s;obfs-host=%s", str(opts, "mode"), str(opts, "host"))
			case "v2ray-plugin":
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
				n.Options["plugin"] = "v2ray-plugin"
				n.Options["plugin_opts"] = s
			case "shadow-tls":
				// ss 以 shadowtls 出站为 detour，由后者负责与服务器握手。
				ver, _ := intv(opts, "version")
				if ver == 0 {
					ver = 2
				}
				st := ir.Node{Tag: name + "/shadow-tls", Protocol: "shadowtls", Server: n.Server, Port: port,
					Options: map[string]any{"version": ver}, Source: src,
					TLS: &ir.TLS{ServerName: str(opts, "host"), Fingerprint: str(p, "client-fingerprint")}}
				if pw := str(opts, "password"); pw != "" {
					st.Options["password"] = pw
				}
				extra = append(extra, st)
				n.Detour = st.Tag
				n.Server, n.Port = "", 0
				fpUsed = true
			default:
				c.warn("node", "ss+"+pl, ir.Unsupported, "ss 插件 "+pl+" 无对应", name, src)
				return main, nil, false
			}
		}
		if str(p, "client-fingerprint") != "" && !fpUsed {
			reasons = append(reasons, "ss 的 client-fingerprint 仅对 shadow-tls 生效，已忽略")
		}
	case "vmess":
		n.Options["uuid"] = str(p, "uuid")
		if v, ok := intv(p, "alterId"); ok {
			n.Options["alter_id"] = v
		}
		if v := str(p, "cipher"); v != "" {
			n.Options["security"] = v
		}
		if v := str(p, "packet-encoding"); v != "" {
			n.Options["packet_encoding"] = v
		}
		if v, ok := boolv(p, "global-padding"); ok {
			n.Options["global_padding"] = v
		}
		if v, ok := boolv(p, "authenticated-length"); ok {
			n.Options["authenticated_length"] = v
		}
		tcpOnly()
	case "vless":
		n.Options["uuid"] = str(p, "uuid")
		if v := str(p, "flow"); v != "" {
			n.Options["flow"] = v
		}
		if v := str(p, "packet-encoding"); v != "" {
			n.Options["packet_encoding"] = v
		}
		if e := str(p, "encryption"); e != "" && e != "none" {
			c.warn("node", "vless+encryption", ir.Unsupported, "vless encryption="+e+" 无对应", name, src)
			return main, nil, false
		}
		tcpOnly()
	case "trojan":
		n.Options["password"] = str(p, "password")
		tcpOnly()
	case "anytls":
		n.Options["password"] = str(p, "password")
		if v, ok := intv(p, "idle-session-check-interval"); ok {
			n.Options["idle_session_check_interval"] = fmt.Sprintf("%ds", v)
		}
		if v, ok := intv(p, "idle-session-timeout"); ok {
			n.Options["idle_session_timeout"] = fmt.Sprintf("%ds", v)
		}
		if v, ok := intv(p, "min-idle-session"); ok {
			n.Options["min_idle_session"] = v
		}
		if udpOff {
			reasons = append(reasons, "udp:false 无对应（anytls 不能关闭 UDP）")
		}
	case "hysteria2":
		n.Options["password"] = str(p, "password")
		if ports := str(p, "ports"); ports != "" {
			n.Ports = portRanges(ports)
		}
		if v, ok := intv(p, "hop-interval"); ok {
			n.Options["hop_interval"] = fmt.Sprintf("%ds", v)
		}
		if v := mbps(str(p, "up")); v > 0 {
			n.Options["up_mbps"] = v
		}
		if v := mbps(str(p, "down")); v > 0 {
			n.Options["down_mbps"] = v
		}
		if ob := str(p, "obfs"); ob != "" {
			n.Options["obfs"] = map[string]any{"type": ob, "password": str(p, "obfs-password")}
		}
	case "hysteria":
		if v := str(p, "auth-str"); v != "" {
			n.Options["auth_str"] = v
		}
		if v := mbps(str(p, "up")); v > 0 {
			n.Options["up_mbps"] = v
		}
		if v := mbps(str(p, "down")); v > 0 {
			n.Options["down_mbps"] = v
		}
		if v := str(p, "obfs"); v != "" {
			n.Options["obfs"] = v
		}
		if pr := str(p, "protocol"); pr != "" && pr != "udp" {
			reasons = append(reasons, "protocol="+pr+"（faketcp / wechat-video）无对应")
		}
		if ports := str(p, "ports"); ports != "" {
			n.Ports = portRanges(ports)
		}
	case "tuic":
		n.Options["uuid"] = str(p, "uuid")
		n.Options["password"] = str(p, "password")
		if v := str(p, "congestion-controller"); v != "" {
			n.Options["congestion_control"] = v
		}
		if v := str(p, "udp-relay-mode"); v != "" {
			n.Options["udp_relay_mode"] = v
		}
		if v, ok := boolv(p, "reduce-rtt"); ok {
			n.Options["zero_rtt_handshake"] = v
		}
		if v, ok := intv(p, "heartbeat-interval"); ok {
			n.Options["heartbeat"] = fmt.Sprintf("%dms", v)
		}
	case "socks5":
		n.Options["username"] = str(p, "username")
		n.Options["password"] = str(p, "password")
		tcpOnly()
	case "http":
		n.Options["username"] = str(p, "username")
		n.Options["password"] = str(p, "password")
	case "snell":
		ver, _ := intv(p, "version")
		if ver != 4 && ver != 6 {
			c.warn("node", fmt.Sprintf("snell v%d", ver), ir.Unsupported, "仅支持 snell v4 / v6", name, src)
			return main, nil, false
		}
		n.Options["version"] = ver
		n.Options["psk"] = str(p, "psk")
		if ob := sub(p, "obfs-opts"); ob != nil {
			n.Options["obfs_mode"] = str(ob, "mode")
			n.Options["obfs_host"] = str(ob, "host")
		}
	case "wireguard":
		var addrs []string
		for _, k := range []string{"ip", "ipv6"} {
			if v := str(p, k); v != "" {
				addrs = append(addrs, hostPrefix(v))
			}
		}
		n.Options["address"] = addrs
		n.Options["private_key"] = str(p, "private-key")
		n.Options["public_key"] = str(p, "public-key")
		if v := str(p, "pre-shared-key"); v != "" {
			n.Options["pre_shared_key"] = v
		}
		if ips := strList(p["allowed-ips"]); len(ips) > 0 {
			n.Options["allowed_ips"] = ips
		} else {
			n.Options["allowed_ips"] = []string{"0.0.0.0/0", "::/0"}
		}
		if r, ok := p["reserved"]; ok {
			n.Options["reserved"] = r
		}
		if v, ok := intv(p, "persistent-keepalive"); ok {
			n.Options["persistent_keepalive_interval"] = v
		}
		if v, ok := intv(p, "mtu"); ok {
			n.Options["mtu"] = v
		}
	}

	if needTLS(typ, p) {
		t, rs := convertTLS(p)
		reasons = append(reasons, rs...)
		n.TLS = t
	}
	if typ == "vmess" || typ == "vless" || typ == "trojan" {
		t, rs := convertTransport(p)
		reasons = append(reasons, rs...)
		n.Transport = t
	}
	for _, k := range unmapped {
		reasons = append(reasons, "字段 "+k+" 未映射")
	}
	for _, r := range reasons {
		c.warn("node", typ, ir.Degraded, r, name, src)
	}
	return n, extra, true
}

func needTLS(typ string, p obj) bool {
	switch typ {
	case "trojan", "anytls", "hysteria", "hysteria2", "tuic":
		return true
	}
	b, _ := boolv(p, "tls")
	return b || sub(p, "reality-opts") != nil
}

func convertTLS(p obj) (*ir.TLS, []string) {
	t := &ir.TLS{ServerName: str(p, "sni", "servername"), ALPN: strList(p["alpn"]), Fingerprint: str(p, "client-fingerprint")}
	if b, _ := boolv(p, "skip-cert-verify"); b {
		t.Insecure = true
	}
	var reasons []string
	if r := sub(p, "reality-opts"); r != nil {
		t.Reality = &ir.Reality{PublicKey: str(r, "public-key"), ShortID: str(r, "short-id")}
		if t.Fingerprint == "" {
			t.Fingerprint = "chrome" // reality 必须启用 uTLS
			reasons = append(reasons, "reality 未指定指纹，默认 chrome")
		}
	}
	return t, reasons
}

func convertTransport(p obj) (*ir.Transport, []string) {
	switch n := str(p, "network"); n {
	case "", "tcp":
		return nil, nil
	case "ws":
		o := sub(p, "ws-opts")
		t := &ir.Transport{Type: "ws", Path: str(o, "path")}
		if h := sub(o, "headers"); h != nil {
			t.Headers = map[string]string{}
			for k, v := range h {
				t.Headers[k] = fmt.Sprint(v)
			}
		}
		if v, ok := intv(o, "max-early-data"); ok {
			t.MaxEarlyData = v
			t.EarlyDataHeader = str(o, "early-data-header-name")
		}
		return t, nil
	case "grpc":
		return &ir.Transport{Type: "grpc", ServiceName: str(sub(p, "grpc-opts"), "grpc-service-name")}, nil
	case "h2":
		o := sub(p, "h2-opts")
		return &ir.Transport{Type: "http", Host: strList(o["host"]), Path: str(o, "path")}, nil
	case "http":
		o := sub(p, "http-opts")
		t := &ir.Transport{Type: "http", Method: str(o, "method")}
		if ps := strList(o["path"]); len(ps) > 0 {
			t.Path = ps[0]
		}
		return t, []string{"http-opts 仅映射首个 path"}
	default:
		return nil, []string{"network=" + n + " 无对应"}
	}
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

// portRanges 把 "20000-30000,40000" 转为 ["20000:30000","40000:40000"]（sing-box 要求均为区间）。
func portRanges(s string) []string {
	var out []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		if !strings.Contains(part, "-") {
			part = part + "-" + part
		}
		out = append(out, strings.ReplaceAll(part, "-", ":"))
	}
	return out
}
