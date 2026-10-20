# 第三方组件

Velum 的发布产物（velumd、vlu、velum-helper、桌面 App、Android App）包含以下主要第三方组件。完整清单见各子项目的依赖文件（`core/go.mod`、`desktop/src-tauri/Cargo.toml`、`desktop/package.json`、`mobile/pubspec.yaml`）。

| 组件 | 用途 | 许可证 |
| :--- | :--- | :--- |
| [sing-box](https://github.com/SagerNet/sing-box) 及 sing、sing-tun、sing-quic 等 SagerNet 库 | 数据面内核 | GPL-3.0-or-later |
| gVisor（SagerNet 分支） | 用户态网络栈 | Apache-2.0 |
| wireguard-go（SagerNet 分支） | WireGuard | MIT |
| golang.org/x/mobile | Android 绑定 | BSD-3-Clause |
| Tauri | 桌面壳 | MIT / Apache-2.0 |
| React | 桌面界面 | MIT |
| Flutter | 移动端界面 | BSD-3-Clause |

sing-box 的许可证附有一条额外条款：衍生作品不得使用其名称或暗示与其存在关联（未经许可）。Velum 是独立项目，与 sing-box 及其作者没有关联，也未获其认可；文中提及 sing-box 仅用于说明所使用的内核。
