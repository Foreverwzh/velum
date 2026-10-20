// velumd：Velum 守护进程。通常由 vlu up / 桌面 App 启动；--foreground 便于调试。
// Windows 上还可作为服务运行（velumd service install，见 service_windows.go）。
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/Foreverwzh/velum/core/pkg/config"
	"github.com/Foreverwzh/velum/core/pkg/daemon"
	"github.com/Foreverwzh/velum/core/pkg/engine/singbox"
	"github.com/Foreverwzh/velum/core/pkg/logfile"
)

func main() {
	if len(os.Args) > 1 && platformCommand(os.Args[1], os.Args[2:]) {
		return
	}
	home := flag.String("home", "", "Velum 目录（默认 $VELUM_HOME，否则 macOS ~/Library/Application Support/Velum、Windows %APPDATA%\\Velum）")
	allowSIDs := flag.String("allow-sid", "", "Windows：允许访问本地接口的用户 SID，逗号分隔（以服务运行时为安装用户）")
	fg := flag.Bool("foreground", false, "日志同时输出到终端")
	version := flag.Bool("version", false, "打印版本")
	flag.Parse()
	if *version {
		fmt.Println(daemon.Version)
		return
	}
	if *home != "" {
		_ = os.Setenv("VELUM_HOME", *home)
	}
	h, err := config.Home()
	if err != nil {
		log.Fatal(err)
	}
	var sids []string
	if *allowSIDs != "" {
		sids = strings.Split(*allowSIDs, ",")
	}
	paths, err := preparePaths(config.PathsFor(h), sids)
	if err != nil {
		log.Fatal(err)
	}
	if err := config.Ensure(paths); err != nil {
		log.Fatal(err)
	}
	lf, err := logfile.Open(paths.Log, 10<<20, 3) // 10MB 轮转，保留 3 份
	if err != nil {
		log.Fatal(err)
	}
	defer lf.Close()
	var out io.Writer = lf
	if *fg {
		out = io.MultiWriter(lf, os.Stderr)
	}
	logger := log.New(out, "[velumd] ", log.LstdFlags|log.Lmicroseconds)
	daemon.RuleSetLoader = singbox.LoadRuleSetFile
	d := daemon.New(paths, singbox.New(singbox.Options{Log: out}), logger)
	d.AllowSIDs = sids
	setupPlatform(d, paths)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP)
	go func() {
		for s := range sig {
			if s == syscall.SIGHUP {
				_ = d.Reload(ctx)
				continue
			}
			d.Shutdown()
		}
	}()
	if err := runDaemon(ctx, d); err != nil {
		logger.Printf("退出：%v", err)
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
