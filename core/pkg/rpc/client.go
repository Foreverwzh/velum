package rpc

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Foreverwzh/velum/core/pkg/engine"
	"github.com/Foreverwzh/velum/core/pkg/ipc"
)

type Client struct {
	http *http.Client
}

// NewClient 依次尝试 addrs（Windows：本用户的 Pipe，其次 Velum 服务的 Pipe），使用第一个可连接的。
func NewClient(addrs ...string) *Client {
	tr := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		var first error
		for _, a := range addrs {
			c, err := ipc.Dial(ctx, a)
			if err == nil {
				return c, nil
			}
			if first == nil {
				first = err
			}
		}
		return nil, first
	}}
	return &Client{http: &http.Client{Transport: tr, Timeout: 30 * time.Second}}
}

func (c *Client) do(method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, _ := json.Marshal(body)
		rd = bytes.NewReader(b)
	}
	req, _ := http.NewRequest(method, "http://velumd"+path, rd)
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		var e ErrorResponse
		_ = json.NewDecoder(resp.Body).Decode(&e)
		if e.Error == "" {
			e.Error = resp.Status
		}
		return fmt.Errorf("%s", e.Error)
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

func (c *Client) Status() (*Status, error) {
	var s Status
	return &s, c.do(http.MethodGet, "/v1/status", nil, &s)
}

func (c *Client) Reload() (*Status, error) {
	var s Status
	return &s, c.do(http.MethodPost, "/v1/reload", nil, &s)
}

func (c *Client) Update() (*Status, error) {
	var s Status
	return &s, c.do(http.MethodPost, "/v1/update", nil, &s)
}

func (c *Client) Stop() error { return c.do(http.MethodPost, "/v1/stop", nil, nil) }

func (c *Client) Switch(req SwitchRequest) (*Status, error) {
	var s Status
	return &s, c.do(http.MethodPost, "/v1/switches", req, &s)
}

func (c *Client) Why(r WhyRequest) (*WhyResponse, error) {
	var w WhyResponse
	return &w, c.do(http.MethodPost, "/v1/why", r, &w)
}

func (c *Client) Connections(limit int, host string) ([]engine.Connection, error) {
	var r ConnectionsResponse
	q := url.Values{"limit": {strconv.Itoa(limit)}}
	if host != "" {
		q.Set("host", host)
	}
	return r.Connections, c.do(http.MethodGet, "/v1/connections?"+q.Encode(), nil, &r)
}

func (c *Client) Select(group, member string) (*Status, error) {
	var s Status
	return &s, c.do(http.MethodPost, "/v1/groups/select", SelectRequest{group, member}, &s)
}

func (c *Client) Test(group string) (*TestResponse, error) {
	var r TestResponse
	return &r, c.do(http.MethodPost, "/v1/groups/test", TestRequest{group}, &r)
}

func (c *Client) Overlay() (*OverlayDoc, error) {
	var d OverlayDoc
	return &d, c.do(http.MethodGet, "/v1/overlay", nil, &d)
}

func (c *Client) OverlayItems() (*OverlayItems, error) {
	var d OverlayItems
	return &d, c.do(http.MethodGet, "/v1/overlay/items", nil, &d)
}

// EditOverlay 添加（op = add）或删除（op = remove）一个 overlay 条目并重新加载。
func (c *Client) EditOverlay(op string, e OverlayEntry) (*Status, error) {
	var s Status
	return &s, c.do(http.MethodPost, "/v1/overlay/items", OverlayEdit{Op: op, Entry: e}, &s)
}

func (c *Client) SaveOverlay(content string) (*Status, error) {
	var s Status
	return &s, c.do(http.MethodPut, "/v1/overlay", OverlayDoc{Content: content}, &s)
}

func (c *Client) Repair() (*RepairResponse, error) {
	var r RepairResponse
	return &r, c.do(http.MethodPost, "/v1/repair", nil, &r)
}

func (c *Client) Warnings() (*WarningsResponse, error) {
	var w WarningsResponse
	return &w, c.do(http.MethodGet, "/v1/warnings", nil, &w)
}

// Stream 订阅连接结束事件（SSE），直到 ctx 取消。
func (c *Client) Stream(ctx context.Context, fn func(engine.Connection)) error {
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, "http://velumd/v1/connections/stream", nil)
	hc := *c.http
	hc.Timeout = 0
	resp, err := hc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	for sc.Scan() {
		line := sc.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var conn engine.Connection
		if json.Unmarshal([]byte(line[6:]), &conn) == nil {
			fn(conn)
		}
	}
	return sc.Err()
}

func (c *Client) Subscription() (*SubscriptionConfig, error) {
	var s SubscriptionConfig
	return &s, c.do(http.MethodGet, "/v1/subscription", nil, &s)
}

func (c *Client) TUNApps() (*TUNApps, error) {
	var r TUNApps
	return &r, c.do(http.MethodGet, "/v1/tun/apps", nil, &r)
}

func (c *Client) SetTUNApps(req TUNApps) (*Status, error) {
	var s Status
	return &s, c.do(http.MethodPut, "/v1/tun/apps", req, &s)
}

func (c *Client) SetSubscription(req SetSubscriptionRequest) (*Status, error) {
	var s Status
	return &s, c.do(http.MethodPut, "/v1/subscription", req, &s)
}

func (c *Client) Profiles() ([]ProfileStatus, error) {
	var ps []ProfileStatus
	return ps, c.do(http.MethodGet, "/v1/profiles", nil, &ps)
}

func (c *Client) AddProfile(req AddProfileRequest) (*Status, error) {
	var s Status
	return &s, c.do(http.MethodPost, "/v1/profiles", req, &s)
}

func (c *Client) EditProfile(id string, req EditProfileRequest) (*Status, error) {
	var s Status
	return &s, c.do(http.MethodPut, "/v1/profiles/"+url.PathEscape(id), req, &s)
}

func (c *Client) DeleteProfile(id string) (*Status, error) {
	var s Status
	return &s, c.do(http.MethodDelete, "/v1/profiles/"+url.PathEscape(id), nil, &s)
}

// ActivateProfile 切换到订阅 id（重新加载，失败时保持原订阅）。
func (c *Client) ActivateProfile(id string) (*Status, error) {
	var s Status
	return &s, c.do(http.MethodPost, "/v1/profiles/"+url.PathEscape(id)+"/activate", nil, &s)
}

// UpdateProfile 重新拉取订阅 id（同时更新流量额度）。
func (c *Client) UpdateProfile(id string) (*Status, error) {
	var s Status
	return &s, c.do(http.MethodPost, "/v1/profiles/"+url.PathEscape(id)+"/update", nil, &s)
}

func (c *Client) Helper() (*HelperStatus, error) {
	var s HelperStatus
	return &s, c.do(http.MethodGet, "/v1/helper", nil, &s)
}
