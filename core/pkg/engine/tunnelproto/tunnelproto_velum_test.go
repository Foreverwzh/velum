package tunnelproto

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/Foreverwzh/velum/core/pkg/engine"
)

// velum-engine：写出引擎文件、meta 标明内核，引擎配置变化时摘要也变。
func TestSaveVelum(t *testing.T) {
	home := t.TempDir()
	base := engine.Config{JSON: []byte(`{}`)}
	h0, err := Save(home, base)
	if err != nil {
		t.Fatal(err)
	}
	c := base
	c.Velum = &engine.VelumConfig{IR: []byte(`{"schema_version":1}`), RuleSets: []byte(`[]`), Meta: []byte(`{"rule_index":[0]}`)}
	h1, err := Save(home, c)
	if err != nil {
		t.Fatal(err)
	}
	if h1 == h0 {
		t.Fatal("加上 velum 配置后摘要应当变化")
	}
	for name, want := range map[string]string{VelumIRFile: `{"schema_version":1}`, VelumRuleSetsFile: `[]`, VelumMetaFile: `{"rule_index":[0]}`} {
		b, err := os.ReadFile(filepath.Join(Dir(home), name))
		if err != nil || string(b) != want {
			t.Errorf("%s = %q, %v", name, b, err)
		}
	}
	var m meta
	b, _ := os.ReadFile(filepath.Join(Dir(home), metaFile))
	if err := json.Unmarshal(b, &m); err != nil || m.Engine != EngineVelum || m.Hash != h1 {
		t.Fatalf("meta：%+v %v", m, err)
	}
	c.Velum = &engine.VelumConfig{IR: []byte(`{"schema_version":1,"ipv6":true}`), RuleSets: []byte(`[]`), Meta: []byte(`{}`)}
	if h2, _ := Save(home, c); h2 == h1 {
		t.Fatal("velum IR 变化时摘要应当变化")
	}
	if h3, _ := Save(home, base); h3 != h0 {
		t.Fatal("去掉 velum 后摘要应当回到原值")
	}
}
