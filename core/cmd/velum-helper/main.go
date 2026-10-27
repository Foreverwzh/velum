//go:build darwin

// velum-helper：Velum 的特权 helper（需要 root）。负责创建 utun、配置路由，并把 utun 交给用户态 velumd。
//
// 开发阶段手动启动：sudo velum-helper（M2 起由桌面 App 通过 SMAppService 注册为 LaunchDaemon）。
package main

import (
	"flag"
	"fmt"
	"log"
	"net"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/Foreverwzh/velum/core/pkg/helper"
)

func main() {
	sock := flag.String("socket", helper.DefaultSocket, "监听的 Unix socket")
	allowUID := flag.Int("allow-uid", -1, "允许连接的用户 uid（默认取 SUDO_UID）")
	flag.Parse()
	logger := log.New(os.Stderr, "[velum-helper] ", log.LstdFlags|log.Lmicroseconds)
	if os.Geteuid() != 0 {
		logger.Fatal("需要 root：sudo velum-helper")
	}
	if *allowUID < 0 {
		v, err := strconv.Atoi(os.Getenv("SUDO_UID"))
		if err != nil {
			logger.Fatal("无法确定允许的用户，请指定 --allow-uid")
		}
		*allowUID = v
	}
	if c, err := net.DialTimeout("unix", *sock, 300*time.Millisecond); err == nil {
		c.Close()
		logger.Fatalf("已有 velum-helper 在运行（%s）", *sock)
	}
	_ = os.Remove(*sock)
	ln, err := net.ListenUnix("unix", &net.UnixAddr{Name: *sock, Net: "unix"})
	if err != nil {
		logger.Fatal(err)
	}
	// 任何用户可连接，鉴权在 accept 后按对端 uid 进行
	_ = os.Chmod(*sock, 0o666)
	srv := &helper.Server{Sys: helper.DarwinSystem{}, AllowUID: *allowUID, Logger: logger, Requirement: helper.PeerRequirement()}
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		s := <-sig
		srv.Shutdown(fmt.Sprintf("收到信号 %s", s))
		ln.Close()
	}()
	logger.Printf("监听 %s，允许 uid=%d（协议 %s），签名要求：%s", *sock, *allowUID, helper.Version, orNone(srv.Requirement))
	_ = srv.Serve(ln)
	_ = os.Remove(*sock)
	logger.Printf("已退出")
}

func orNone(s string) string {
	if s == "" {
		return "无（未签名构建，只按 uid 鉴权）"
	}
	return s
}
