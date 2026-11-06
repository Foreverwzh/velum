package ir

import "testing"

func TestNetPolicyPick(t *testing.T) {
	p := &NetPolicy{SSID: map[string]string{"家": "DIRECT"}, Cellular: "WG", Default: "WG"}
	for _, c := range []struct {
		kind, ssid, want string
		ok               bool
	}{
		{NetWiFi, "家", "DIRECT", true},
		{NetWiFi, "咖啡馆", "WG", true},
		{NetCellular, "", "WG", true},
		{NetWiFi, "", "", false}, // 系统没给名称：不能当成「其他 Wi-Fi」，否则在家也会走 WG
		{"", "", "", false},
	} {
		if m, ok := p.Pick(c.kind, c.ssid); m != c.want || ok != c.ok {
			t.Errorf("Pick(%q, %q) = %q, %v，应为 %q, %v", c.kind, c.ssid, m, ok, c.want, c.ok)
		}
	}
	// 没有 default、cellular 时不触发切换（与 Stash 相同）
	q := &NetPolicy{SSID: map[string]string{"家": "DIRECT"}}
	if _, ok := q.Pick(NetWiFi, "咖啡馆"); ok {
		t.Error("没有 default 不应切换")
	}
	if _, ok := q.Pick(NetCellular, ""); ok {
		t.Error("没有 cellular 不应切换")
	}
	// 只写了 default：蜂窝也用它（家里直连、其他走节点）
	home := &NetPolicy{SSID: map[string]string{"家": "DIRECT"}, Default: "WG"}
	if m, ok := home.Pick(NetCellular, ""); !ok || m != "WG" {
		t.Errorf("没写 cellular 时蜂窝应当用 default：%q %v", m, ok)
	}
	if m, ok := home.Pick(NetWiFi, "家"); !ok || m != "DIRECT" {
		t.Errorf("在家应当直连：%q %v", m, ok)
	}
	// 桌面端有线网络：不在家
	if m, ok := home.Pick(NetWired, ""); !ok || m != "WG" {
		t.Errorf("有线网络应当用 default：%q %v", m, ok)
	}
	got := map[string]string{}
	ApplyNetPolicies(map[string]*NetPolicy{"Home": home, "空": {}}, NetWiFi, "家", func(g, m string) error { got[g] = m; return nil })
	if len(got) != 1 || got["Home"] != "DIRECT" {
		t.Errorf("ApplyNetPolicies 结果不对：%v", got)
	}
	var nilP *NetPolicy
	if _, ok := nilP.Pick(NetWiFi, "家"); ok {
		t.Error("没有策略不应切换")
	}
}
