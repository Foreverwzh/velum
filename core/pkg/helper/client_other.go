//go:build !unix

package helper

import "errors"

// Windows 上没有 velum-helper：velumd 以服务运行，sing-box 自行创建 wintun 并配置路由。
var errUnsupported = errors.New("velum-helper 仅用于 macOS")

type Client struct{}

func Dial(string) (*Client, error) { return nil, errUnsupported }

func (*Client) OpenTun(uint32, []string, []string, []string) (int, string, error) {
	return -1, "", errUnsupported
}
func (*Client) SetDNS(...string) error                { return errUnsupported }
func (*Client) AddRoutes(...string) ([]string, error) { return nil, errUnsupported }
func (*Client) DelRoutes(...string) ([]string, error) { return nil, errUnsupported }
func (*Client) Close() error                          { return nil }
