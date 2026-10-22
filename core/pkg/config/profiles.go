package config

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"strings"

	"github.com/Foreverwzh/velum/core/pkg/userfs"
	"gopkg.in/yaml.v3"
)

// Profile 是一个保存的订阅：远程地址（url）或本地文件（file）二选一。
type Profile struct {
	Name string `yaml:"name,omitempty"` // 为空时显示订阅服务提供的名称（profile-title）或地址的主机名
	URL  string `yaml:"url,omitempty"`
	File string `yaml:"file,omitempty"`
}

// ID 是订阅的稳定标识（由地址 / 文件路径得出），用于接口与托盘菜单，不暴露地址中的 token。
func (p Profile) ID() string { return ProfileID(p.URL, p.File) }

// ProfileID 返回 url 或 file 对应的订阅标识。
func ProfileID(url, file string) string {
	key := "url:" + url
	if url == "" {
		key = "file:" + file
	}
	h := sha256.Sum256([]byte(key))
	return hex.EncodeToString(h[:6])
}

const profilesHeader = `# Velum 订阅列表：在界面「设置 → 订阅」或托盘菜单中添加、切换；也可以直接编辑。
# 当前使用的订阅写在 config.yaml 的 subscription 中（url / file），切换时由 Velum 改写。
`

// LoadProfiles 读取订阅列表；文件不存在时返回空列表。
func LoadProfiles(path string) ([]Profile, error) {
	data, err := userfs.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var f struct {
		Profiles []Profile `yaml:"profiles"`
	}
	if err := yaml.Unmarshal(data, &f); err != nil {
		return nil, err
	}
	var out []Profile
	for _, p := range f.Profiles {
		p.Name, p.URL, p.File = strings.TrimSpace(p.Name), strings.TrimSpace(p.URL), strings.TrimSpace(p.File)
		if p.URL != "" || p.File != "" {
			out = append(out, p)
		}
	}
	return out, nil
}

// SaveProfiles 写入订阅列表。
func SaveProfiles(path string, ps []Profile) error {
	data, err := yaml.Marshal(struct {
		Profiles []Profile `yaml:"profiles"`
	}{ps})
	if err != nil {
		return err
	}
	return userfs.WriteFileAtomic(path, append([]byte(profilesHeader), data...), 0o600)
}

// FindProfile 返回 id 对应的下标，没有时为 -1。
func FindProfile(ps []Profile, id string) int {
	for i, p := range ps {
		if p.ID() == id {
			return i
		}
	}
	return -1
}
