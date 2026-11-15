# 第三方组件

Velum 的发布产物（velumd、vlu、velum-helper、桌面 App、移动 App）包含以下主要第三方组件。

| 组件 | 用途 | 许可证 |
| :--- | :--- | :--- |
| velum-engine 依赖的 Rust crate（BoringSSL、tokio、ipstack 等） | 核心与数据面 | 见安装包内的 THIRD_PARTY_RUST.txt（各 crate 的许可证全文） |
| [Wintun](https://www.wintun.net)（WireGuard LLC） | Windows 的 TUN 驱动（wintun.dll，原样随附） | Wintun 预编译二进制许可（安装目录内的 wintun-LICENSE.txt） |
| Tauri | 桌面壳 | MIT / Apache-2.0 |
| React | 桌面界面 | MIT |
| Flutter | Android 界面 | BSD-3-Clause |

完整清单见各子项目的依赖文件（`desktop/src-tauri/Cargo.toml`、`desktop/package.json`、`mobile/pubspec.yaml`）与 THIRD_PARTY_RUST.txt。
