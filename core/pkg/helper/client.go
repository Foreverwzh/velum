//go:build unix

package helper

import (
	"errors"
	"fmt"
	"net"
	"sync"
	"time"
)

// Client 是 velumd 侧的 helper 客户端。连接保持期间 helper 持有的路由与 utun 有效；
// 连接断开（含进程退出 / 被杀）时 helper 自动清理。
type Client struct {
	mu   sync.Mutex
	conn *net.UnixConn
}

func Dial(socket string) (*Client, error) {
	c, err := net.DialUnix("unix", nil, &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		return nil, fmt.Errorf("连接 velum-helper（%s）：%w；TUN 模式需要先以 root 启动 helper：sudo velum-helper", socket, err)
	}
	cl := &Client{conn: c}
	resp, _, err := cl.call(Request{Op: OpPing})
	if err != nil {
		c.Close()
		return nil, err
	}
	if resp.Version != Version {
		c.Close()
		return nil, fmt.Errorf("%w：helper=%s velumd=%s，请更新 velum-helper", ErrVersionMismatch, resp.Version, Version)
	}
	return cl, nil
}

func (c *Client) call(req Request) (Response, int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return Response{}, -1, errors.New("helper 连接已关闭")
	}
	if err := send(c.conn, req, -1); err != nil {
		return Response{}, -1, err
	}
	var resp Response
	fd, err := recv(c.conn, &resp)
	if err != nil {
		return resp, -1, err
	}
	if !resp.OK {
		return resp, fd, fmt.Errorf("helper: %s", resp.Error)
	}
	return resp, fd, nil
}

// OpenTun 让 helper 创建并配置 utun，返回其 FD（由调用方负责关闭）与名称。
func (c *Client) OpenTun(mtu uint32, inet4, inet6, routes []string) (fd int, name string, err error) {
	resp, fd, err := c.call(Request{Op: OpOpenTun, MTU: mtu, Inet4: inet4, Inet6: inet6, Routes: routes})
	if err != nil {
		return -1, "", err
	}
	if fd < 0 {
		return -1, "", errors.New("helper 未返回 TUN 文件描述符")
	}
	return fd, resp.Name, nil
}

// SetDNS 让 helper 接管系统 DNS；会话断开时 helper 自动还原。
func (c *Client) SetDNS(servers ...string) error {
	_, _, err := c.call(Request{Op: OpSetDNS, DNS: servers})
	return err
}

// ResetDNS 让 helper 把系统 DNS 含有这些地址的网络服务改回自动获取，返回改动的服务。
func (c *Client) ResetDNS(servers ...string) ([]string, error) {
	resp, _, err := c.call(Request{Op: OpResetDNS, DNS: servers})
	return resp.Services, err
}

func (c *Client) AddRoutes(routes ...string) ([]string, error) {
	resp, _, err := c.call(Request{Op: OpAddRoutes, Routes: routes})
	return resp.Routes, err
}

func (c *Client) DelRoutes(routes ...string) ([]string, error) {
	resp, _, err := c.call(Request{Op: OpDelRoutes, Routes: routes})
	return resp.Routes, err
}

// Close 关闭会话：先请求 helper 同步清理（还原 DNS、删除路由、关闭 utun）并等待完成，再断开连接。
// 清理请求失败时（helper 无响应等）直接断开，helper 仍会在断开后清理。
func (c *Client) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return nil
	}
	_ = c.conn.SetDeadline(time.Now().Add(5 * time.Second))
	if err := send(c.conn, Request{Op: OpClose}, -1); err == nil {
		var resp Response
		_, _ = recv(c.conn, &resp)
	}
	err := c.conn.Close()
	c.conn = nil
	return err
}
