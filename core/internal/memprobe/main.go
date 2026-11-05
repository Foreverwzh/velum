// memprobe 测量内核单独运行时的内存，用于评估 iOS 网络扩展的内存上限（不开 TUN，不改系统设置）。
//
//	memprobe -dump out.json -home <隔离目录>   用完整的 daemon 流程生成内核配置，写到文件后退出
//	memprobe -cfg out.json [-settle 30s] [-heap out.pprof]   只运行内核，输出内存读数
package main

import (
	"context"
	"encoding/json"
	"flag"
	"io"
	"fmt"
	"log"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"runtime/debug"
	"runtime/pprof"
	"strconv"
	"time"

	"github.com/Foreverwzh/velum/core/pkg/config"
	"github.com/Foreverwzh/velum/core/pkg/daemon"
	"github.com/Foreverwzh/velum/core/pkg/engine"
	"github.com/Foreverwzh/velum/core/pkg/engine/singbox"
)

type capture struct {
	engine.Engine
	done chan []byte
}

func (c capture) Start(_ context.Context, cfg engine.Config) error {
	c.done <- cfg.JSON
	select {}
}

type dummyOpener struct{}

func (dummyOpener) OpenTUN(engine.TUNRequest) (int, string, io.Closer, error) {
	return -1, "", nil, nil
}

var fp = regexp.MustCompile(`phys_footprint:\s+([\d.]+) (KB|MB)`)

func footprint() string {
	out, err := exec.Command("footprint", strconv.Itoa(os.Getpid())).CombinedOutput()
	if err != nil {
		return "?"
	}
	if m := fp.FindStringSubmatch(string(out)); m != nil {
		return m[1] + m[2]
	}
	return "?"
}

func report(tag string) {
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	fmt.Printf("%-10s footprint %-8s heapInuse %3dM heapObj %8d stack %2dM sys %3dM goroutine %d gc %d\n", tag, footprint(),
		m.HeapInuse>>20, m.HeapObjects, m.StackInuse>>20, m.Sys>>20, runtime.NumGoroutine(), m.NumGC)
}

func main() {
	home := flag.String("home", "", "隔离目录")
	dump := flag.String("dump", "", "生成配置并写到这里")
	cfgPath := flag.String("cfg", "", "配置文件")
	settle := flag.Duration("settle", 30*time.Second, "运行多久")
	heap := flag.String("heap", "", "堆分析输出")
	flag.Parse()
	daemon.RuleSetLoader = singbox.LoadRuleSetFile
	if *dump != "" {
		done := make(chan []byte, 1)
		paths := config.PathsFor(*home)
		paths.Socket = fmt.Sprintf("/tmp/vmp-%d.sock", os.Getpid()) // 隔离目录的路径可能超过 Unix socket 的长度上限
		defer os.Remove(paths.Socket)
		d := daemon.New(paths, capture{Engine: singbox.New(singbox.Options{}), done: done}, log.New(os.Stderr, "", 0))
		d.TUNOpener = dummyOpener{} // 生成隧道模式的配置，不打开 TUN
		go func() {
			if err := d.Run(context.Background()); err != nil {
				log.Fatal(err)
			}
		}()
		select {
		case b := <-done:
			if err := os.WriteFile(*dump, b, 0o600); err != nil {
				log.Fatal(err)
			}
			fmt.Println("已写配置", len(b), "字节")
		case <-time.After(3 * time.Minute):
			log.Fatal("超时")
		}
		return
	}
	b, err := os.ReadFile(*cfgPath)
	if err != nil {
		log.Fatal(err)
	}
	report("启动前")
	e := singbox.New(singbox.Options{})
	if err := e.Start(context.Background(), engine.Config{JSON: b, LogLevel: "error", CachePath: *cfgPath + ".db"}); err != nil {
		log.Fatal(err)
	}
	report("内核启动后")
	time.Sleep(*settle)
	report("运行后")
	runtime.GC()
	debug.FreeOSMemory()
	time.Sleep(time.Second)
	report("回收后")
	if *heap != "" {
		f, _ := os.Create(*heap)
		_ = pprof.WriteHeapProfile(f)
		f.Close()
	}
	var cfg map[string]any
	_ = json.Unmarshal(b, &cfg)
	if r, ok := cfg["route"].(map[string]any); ok {
		rs, _ := r["rule_set"].([]any)
		var local, inline int
		for _, x := range rs {
			switch x.(map[string]any)["type"] {
			case "inline":
				inline++
			default:
				local++
			}
		}
		rules, _ := r["rules"].([]any)
		ob, _ := cfg["outbounds"].([]any)
		fmt.Printf("配置：%d KB，出站 %d，规则 %d，规则集 %d（内联 %d）；find_process=%v\n", len(b)>>10, len(ob), len(rules), local+inline, inline, r["find_process"])
	}
	_ = e.Stop()
}
