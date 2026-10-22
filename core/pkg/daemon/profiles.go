package daemon

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Foreverwzh/velum/core/pkg/config"
	"github.com/Foreverwzh/velum/core/pkg/rpc"
	"github.com/Foreverwzh/velum/core/pkg/source"
	"github.com/Foreverwzh/velum/core/pkg/subscription"
	"github.com/Foreverwzh/velum/core/pkg/userfs"
)

// ---------------- 订阅列表（profiles.yaml） ----------------
//
// config.yaml 的 subscription（url / file）始终是当前使用的订阅；profiles.yaml 保存所有订阅，切换即改写前者。
// 当前订阅不在列表中时（如首次升级、手动编辑了 config.yaml）自动加入，列表因此总是包含它。

// syncProfiles 读取订阅列表（必要时加入当前订阅），返回状态与当前订阅的名称。
func (d *Daemon) syncProfiles(cfg *config.Config, f *source.Fetcher, info *source.Info) ([]rpc.ProfileStatus, string) {
	ps, err := config.LoadProfiles(d.Paths.Profiles)
	if err != nil {
		d.Logger.Printf("读取订阅列表失败：%v", err)
		return nil, ""
	}
	active := ""
	if cfg.Subscription.URL != "" || cfg.Subscription.File != "" {
		active = config.ProfileID(cfg.Subscription.URL, cfg.Subscription.File)
		if config.FindProfile(ps, active) < 0 {
			p := config.Profile{URL: cfg.Subscription.URL, File: cfg.Subscription.File}
			if cfg.Subscription.URL != "" {
				p.File = ""
			}
			if info != nil {
				p.Name = info.Title // 为空时显示订阅服务提供的名称（拉取后才有）或主机名
			}
			ps = append(ps, p)
			if err := config.SaveProfiles(d.Paths.Profiles, ps); err != nil {
				d.Logger.Printf("保存订阅列表失败：%v", err)
			}
		}
	}
	out := make([]rpc.ProfileStatus, 0, len(ps))
	name := ""
	for _, p := range ps {
		s := rpc.ProfileStatus{ID: p.ID(), Name: p.Name, Active: p.ID() == active}
		if p.URL != "" {
			s.Kind, s.Source, s.Info = "url", redact(p.URL), f.Info("subscription", p.URL)
			if st, err := os.Stat(f.CachePath("subscription", p.URL)); err == nil {
				s.FetchedAt = st.ModTime()
			}
		} else {
			s.Kind, s.Source, s.FetchedAt = "file", p.File, modTime(p.File)
		}
		if s.Name == "" {
			s.Name = defaultProfileName(p, s.Info)
		}
		if s.Active {
			name = s.Name
		}
		out = append(out, s)
	}
	return out, name
}

// defaultProfileName：订阅服务提供的名称，否则地址的主机名 / 文件名。
func defaultProfileName(p config.Profile, info *source.Info) string {
	if info != nil && info.Title != "" {
		return info.Title
	}
	if u, err := url.Parse(p.URL); err == nil && p.URL != "" {
		return u.Hostname()
	}
	return filepath.Base(p.File)
}

// verifySubscription 拉取并解析订阅地址（忽略缓存），必须有可用节点；返回订阅服务提供的信息。
func (d *Daemon) verifySubscription(cfg *config.Config, rawURL, ua string) (*source.Info, error) {
	if u, err := url.Parse(rawURL); err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, fmt.Errorf("订阅地址应以 http:// 或 https:// 开头")
	}
	f := source.New(d.Paths.Cache, ua)
	f.TTL, f.Log = 0, d.Logger.Printf
	if d.Engine.Running() {
		f.Fallback = &url.URL{Scheme: "http", Host: cfg.ListenAddr().String()}
	}
	start := time.Now()
	defer func() { d.Logger.Printf("拉取并校验订阅：用时 %.1fs", time.Since(start).Seconds()) }()
	data, res, err := f.Get("subscription", rawURL, 0)
	if err == nil && res.Err != "" {
		err = errors.New(res.Err) // 拉取失败、回落到了旧缓存：校验时不算成功
	}
	if err != nil {
		return nil, fmt.Errorf("拉取订阅失败：%w", err)
	}
	sub, err := subscription.Parse(data, subscription.Options{File: "subscription", ReadFile: f.ProviderReader(), GeoRuleSetURL: cfg.Subscription.GeoRuleSetURL})
	if err != nil {
		return nil, fmt.Errorf("订阅内容无法识别（需要 Clash / Stash / mihomo 格式）：%w", err)
	}
	if len(sub.Nodes) == 0 {
		return nil, fmt.Errorf("订阅中没有可用节点")
	}
	return f.Info("subscription", rawURL), nil
}

// useSubscription 把 config.yaml 的 subscription 改为 url 或 file 并重新加载；失败时改回原配置。
func (d *Daemon) useSubscription(ctx context.Context, rawURL, file string) error {
	old, err := userfs.ReadFile(d.Paths.Config)
	if err != nil {
		return err
	}
	data, err := config.SetFields(old, "subscription", map[string]string{"url": rawURL, "file": file}, []string{"url", "file"})
	if err == nil {
		err = userfs.WriteFileAtomic(d.Paths.Config, data, 0o600)
	}
	if err != nil {
		return err
	}
	if err := d.Reload(ctx); err != nil {
		_ = userfs.WriteFileAtomic(d.Paths.Config, old, 0o600)
		_ = d.Reload(ctx)
		return fmt.Errorf("应用失败，已改回原订阅：%w", err)
	}
	return nil
}

func (d *Daemon) getProfiles(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, d.status().Profiles)
}

func (d *Daemon) addProfile(w http.ResponseWriter, r *http.Request) {
	var req rpc.AddProfileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	req.URL, req.Name = strings.TrimSpace(req.URL), strings.TrimSpace(req.Name)
	cfg, err := config.Load(d.Paths)
	if err != nil {
		writeErr(w, http.StatusUnprocessableEntity, err)
		return
	}
	ps, err := config.LoadProfiles(d.Paths.Profiles)
	if err != nil {
		writeErr(w, http.StatusUnprocessableEntity, err)
		return
	}
	p := config.Profile{Name: req.Name, URL: req.URL}
	if i := config.FindProfile(ps, p.ID()); i >= 0 {
		writeErr(w, http.StatusConflict, fmt.Errorf("这个订阅已在列表中（%s）", ps[i].Name))
		return
	}
	info, err := d.verifySubscription(cfg, req.URL, cfg.Subscription.UserAgent)
	if err != nil {
		writeErr(w, http.StatusUnprocessableEntity, err)
		return
	}
	if p.Name == "" && info != nil {
		p.Name = info.Title
	}
	if err := config.SaveProfiles(d.Paths.Profiles, append(ps, p)); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if req.Activate {
		err = d.useSubscription(r.Context(), p.URL, "")
	} else {
		err = d.Reload(r.Context())
	}
	if err != nil {
		writeErr(w, http.StatusUnprocessableEntity, err)
		return
	}
	writeJSON(w, d.status())
}

// profileAt 读取订阅列表并找到 id 对应的一项。
func (d *Daemon) profileAt(w http.ResponseWriter, id string) ([]config.Profile, int, bool) {
	ps, err := config.LoadProfiles(d.Paths.Profiles)
	if err != nil {
		writeErr(w, http.StatusUnprocessableEntity, err)
		return nil, -1, false
	}
	i := config.FindProfile(ps, id)
	if i < 0 {
		writeErr(w, http.StatusNotFound, fmt.Errorf("订阅不存在"))
		return nil, -1, false
	}
	return ps, i, true
}

func (d *Daemon) isActive(p config.Profile) bool {
	d.mu.RLock()
	defer d.mu.RUnlock()
	return d.st != nil && d.st.sub.ID == p.ID()
}

func (d *Daemon) editProfile(w http.ResponseWriter, r *http.Request) {
	var req rpc.EditProfileRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	ps, i, ok := d.profileAt(w, r.PathValue("id"))
	if !ok {
		return
	}
	active := d.isActive(ps[i])
	if n := strings.TrimSpace(req.Name); n != "" {
		ps[i].Name = n
	}
	newURL := strings.TrimSpace(req.URL)
	if newURL != "" && newURL != ps[i].URL {
		if j := config.FindProfile(ps, config.ProfileID(newURL, "")); j >= 0 {
			writeErr(w, http.StatusConflict, fmt.Errorf("这个地址已在列表中（%s）", ps[j].Name))
			return
		}
		cfg, err := config.Load(d.Paths)
		if err != nil {
			writeErr(w, http.StatusUnprocessableEntity, err)
			return
		}
		if _, err := d.verifySubscription(cfg, newURL, cfg.Subscription.UserAgent); err != nil {
			writeErr(w, http.StatusUnprocessableEntity, err)
			return
		}
		ps[i].URL, ps[i].File = newURL, ""
	} else {
		newURL = ""
	}
	if err := config.SaveProfiles(d.Paths.Profiles, ps); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	var err error
	if active && newURL != "" {
		err = d.useSubscription(r.Context(), newURL, "")
	} else {
		err = d.Reload(r.Context())
	}
	if err != nil {
		writeErr(w, http.StatusUnprocessableEntity, err)
		return
	}
	writeJSON(w, d.status())
}

func (d *Daemon) deleteProfile(w http.ResponseWriter, r *http.Request) {
	ps, i, ok := d.profileAt(w, r.PathValue("id"))
	if !ok {
		return
	}
	if d.isActive(ps[i]) {
		writeErr(w, http.StatusConflict, fmt.Errorf("正在使用的订阅不能删除，请先切换到其他订阅"))
		return
	}
	if err := config.SaveProfiles(d.Paths.Profiles, append(ps[:i:i], ps[i+1:]...)); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if err := d.Reload(r.Context()); err != nil {
		writeErr(w, http.StatusUnprocessableEntity, err)
		return
	}
	writeJSON(w, d.status())
}

func (d *Daemon) activateProfile(w http.ResponseWriter, r *http.Request) {
	ps, i, ok := d.profileAt(w, r.PathValue("id"))
	if !ok {
		return
	}
	if !d.isActive(ps[i]) {
		start := time.Now()
		if err := d.useSubscription(r.Context(), ps[i].URL, ps[i].File); err != nil {
			writeErr(w, http.StatusUnprocessableEntity, err)
			return
		}
		d.Logger.Printf("切换订阅：%s，用时 %.1fs", ps[i].Name, time.Since(start).Seconds())
	}
	writeJSON(w, d.status())
}

// updateProfile 重新拉取订阅（同时更新流量额度）：正在使用的订阅与 vlu update 相同；其他订阅只拉取并校验。
func (d *Daemon) updateProfile(w http.ResponseWriter, r *http.Request) {
	ps, i, ok := d.profileAt(w, r.PathValue("id"))
	if !ok {
		return
	}
	var err error
	switch {
	case d.isActive(ps[i]):
		err = d.Update(r.Context())
	case ps[i].URL != "":
		var cfg *config.Config
		if cfg, err = config.Load(d.Paths); err == nil {
			if _, err = d.verifySubscription(cfg, ps[i].URL, cfg.Subscription.UserAgent); err == nil {
				err = d.Reload(r.Context())
			}
		}
	}
	if err != nil {
		writeErr(w, http.StatusUnprocessableEntity, err)
		return
	}
	writeJSON(w, d.status())
}
