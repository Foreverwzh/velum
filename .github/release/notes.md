> 预发布版本，用于测试（macOS 版已签名并公证，Windows 版未签名）。问题反馈请附上「诊断」页导出的报告或日志。

### 安装

| 平台 | 文件 | 说明 |
| :--- | :--- | :--- |
| macOS（Apple 芯片） | `Velum-*-macos-arm64.zip` | 解压后把 Velum.app 拖进「应用程序」 |
| Windows（x64） | `Velum-*-windows-x64-setup.exe` | SmartScreen 提示时点「更多信息 → 仍要运行」。开启 TUN 需要在应用内安装 Velum 服务（会请求管理员权限） |
| Android 10+ | `Velum-*-android-arm64-v8a.apk` | 绝大多数手机选这个；老旧 32 位设备用 `armeabi-v7a`，模拟器用 `x86_64`。需允许「安装未知应用」 |
| Linux（x64、arm64） | `Velum-*-linux-{x64,arm64}.tar.gz` | 命令行版：解压后运行 `./vlu up`，只提供本地端口（`vlu env` 输出代理环境变量），暂不支持 TUN 和系统服务。校验和见 `SHA256SUMS` |

macOS 首次开启 TUN 时会安装 TUN 组件（需要管理员密码）；从旧版本升级后，应用会提示更新该组件。

### 实验：网络扩展（macOS）

「设置 → 系统组件 → 网络扩展」可以改用系统网络扩展运行 Velum：不需要 TUN 组件，Velum 退出或崩溃时系统自动撤销路由、DNS 与系统代理，与企业 VPN 共存更稳。默认关闭。首次开启需要在「系统设置」中允许 Velum 的网络扩展并允许添加 VPN 配置；开启时以当前配置重新迁移，关闭后在扩展中改过的设置不会带回。无法启动时会自动退回 TUN 组件。**已知问题**：开着 Velum 再连接企业 VPN（同样基于网络扩展的 VPN 客户端）时，企业 VPN 可能连不上；需要同时使用企业 VPN 时请暂不开启。
