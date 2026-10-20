# Velum core

Go 核心：订阅转换 → VelumIR → 编译（Overlay）→ sing-box 渲染 → 引擎 + 动态实录；`velumd` 守护进程与 `vlu` 命令行。

```bash
make build          # bin/velumd、bin/vlu（构建标签见 Makefile）
make test           # 全部测试；VELUM_TEST_STASH=1 额外用本机 Stash 配置做端到端回归
make golden         # 渲染结果变更后更新 golden

./bin/vlu up        # 首次运行生成 config.yaml / overlay.yaml（默认 ~/Library/Application Support/Velum，可用 VELUM_HOME 覆盖）
./bin/vlu status
./bin/vlu why https://example.com/ --live
./bin/vlu conns -f
./bin/vlu down
```

默认只监听 `127.0.0.1:7899`（`mode: proxy`），不改动系统代理。
