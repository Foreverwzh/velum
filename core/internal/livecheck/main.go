// livecheck：系统代理真机验证（第 ① 步）。只操作 -only 指定的闲置服务：接管 → 读取校验 → 还原 → 读取比对。
package main

import (
	"flag"
	"fmt"
	"os"
	"reflect"
	"time"

	"github.com/Foreverwzh/velum/core/pkg/compiler"
	"github.com/Foreverwzh/velum/core/pkg/sysproxy"
)

func norm(ss []sysproxy.Service) []sysproxy.Service {
	for i := range ss {
		for _, p := range []*sysproxy.Proxy{&ss[i].Web, &ss[i].SecureWeb, &ss[i].Socks} {
			if !p.Enabled {
				*p = sysproxy.Proxy{}
			}
		}
	}
	return ss
}

func main() {
	only := flag.String("only", "", "只操作的服务名")
	flag.Parse()
	m := sysproxy.New("/tmp/velum-livecheck.snapshot.json")
	m.Only = []string{*only}
	svcs, err := m.Services()
	fmt.Println("将操作的服务：", svcs, err)
	if len(svcs) != 1 || svcs[0] != *only {
		fmt.Println("服务筛选不符，中止")
		os.Exit(1)
	}
	before, _ := m.Read()
	t := time.Now()
	err = m.Apply(compiler.SysProxyPlan{Enabled: true, Host: "127.0.0.1", Port: 7899, Exceptions: []compiler.Exception{{Pattern: "localhost"}, {Pattern: "*.corp.example.com"}}})
	fmt.Printf("Apply: err=%v（%s）\n", err, time.Since(t).Round(time.Millisecond))
	during, _ := m.Read()
	fmt.Printf("接管中：%+v\n", during)
	t = time.Now()
	err = m.Restore()
	fmt.Printf("Restore: err=%v（%s）快照残留=%v\n", err, time.Since(t).Round(time.Millisecond), m.HasSnapshot())
	after, _ := m.Read()
	fmt.Println("还原后与接管前功能等价：", reflect.DeepEqual(norm(before), norm(after)))
	if !reflect.DeepEqual(norm(before), norm(after)) {
		fmt.Printf("before=%+v\nafter=%+v\n", before, after)
	}
}
