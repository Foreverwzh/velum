package sysproxy

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/Foreverwzh/velum/core/pkg/winuser"
)

// UserSessionBackend 供以服务（SYSTEM）运行的 velumd 使用：WinINet 设置属于用户会话，
// 因此在用户会话中以用户身份启动 `<Exe> sysproxy read|write` 子进程执行读写（见 RunCommand、winuser.Run）。
type UserSessionBackend struct {
	Exe string
	// AllowSIDs 是允许代为设置的用户（安装用户）；为空时使用活动控制台会话的用户。
	AllowSIDs []string
}

func (b UserSessionBackend) Read() (WinSettings, error) {
	var s WinSettings
	out, err := b.run("read", nil)
	if err != nil {
		return s, err
	}
	return s, json.Unmarshal(out, &s)
}

func (b UserSessionBackend) Write(s WinSettings) error {
	in, _ := json.Marshal(s)
	_, err := b.run("write", in)
	return err
}

func (b UserSessionBackend) run(op string, in []byte) ([]byte, error) {
	out, err := winuser.Run(b.AllowSIDs, b.Exe, []string{"sysproxy", op}, in, 15*time.Second)
	if err != nil {
		return nil, fmt.Errorf("在用户会话中设置系统代理（%s）：%w", op, err)
	}
	return out, nil
}

// RunCommand 实现子进程一侧：`sysproxy read` 输出当前设置（JSON），`sysproxy write` 从 stdin 读取设置并写入。
func RunCommand(args []string, stdin []byte) ([]byte, error) {
	if len(args) != 1 {
		return nil, errors.New("用法：sysproxy read|write")
	}
	var b WinINetBackend
	switch args[0] {
	case "read":
		s, err := b.Read()
		if err != nil {
			return nil, err
		}
		return json.Marshal(s)
	case "write":
		var s WinSettings
		if err := json.Unmarshal(stdin, &s); err != nil {
			return nil, err
		}
		return nil, b.Write(s)
	}
	return nil, fmt.Errorf("未知操作：%s", args[0])
}
