> 预发布版本（未签名），用于测试。问题反馈请附上「诊断」页导出的报告或日志。

### 安装

| 平台 | 文件 | 说明 |
| :--- | :--- | :--- |
| macOS（Apple 芯片） | `Velum-*-macos-arm64.zip` | 解压后把 Velum.app 拖进「应用程序」。未签名的应用首次打开会提示「已损坏」或「无法验证开发者」，在终端执行一次：`xattr -dr com.apple.quarantine /Applications/Velum.app` |
| Windows（x64） | `Velum-*-windows-x64-setup.exe` | SmartScreen 提示时点「更多信息 → 仍要运行」。开启 TUN 需要在应用内安装 Velum 服务（会请求管理员权限） |
| Android 10+ | `Velum-*-android-arm64-v8a.apk` | 绝大多数手机选这个；老旧 32 位设备用 `armeabi-v7a`，模拟器用 `x86_64`。需允许「安装未知应用」 |

macOS 首次开启 TUN 时会安装 TUN 组件（需要管理员密码）；从旧版本升级后，应用会提示更新该组件。
