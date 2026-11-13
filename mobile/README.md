# Velum 移动端（Flutter，目前只有 Android）

设计与进度见 `docs/M3-PLAN.md`。Android 10（API 29）及以上。

## 构建

核心是 Rust（私有仓库 velum-engine 的 `crates/velum-android`，与桌面 velumd 相同的 daemon），以 `libvelum_android.so`
随 App 打包；界面经 Unix socket 上的 HTTP 访问它，接口与桌面相同。

需要：Android SDK（含 NDK）、JDK 17+、Flutter、Go（只用来导出核心文字的英文表）。核心的来源：

- 本机有 velum-engine 源码（默认与本仓库同级，或 `VELUM_ENGINE_DIR`）：从源码编译，还需要 Rust 与 Android 目标
  （`rustup target add aarch64-linux-android armv7-linux-androideabi x86_64-linux-android`）；
- 否则按 `android/engine.lock` 下载预编译的（需要 `gh` 与能读 velum-engine 的令牌 `GH_TOKEN`）。

```sh
export ANDROID_HOME=…                          # Android SDK
sh scripts/core.sh                             # 核心 → android/app/src/main/jniLibs/<abi>/（32 / 64 位 ARM、x86_64）
flutter build apk --release --split-per-abi   # 每个架构一个 APK；手机一般用 arm64-v8a
```

开发调试：`ABIS=arm64-v8a sh scripts/core.sh` 只编译一个架构，更快。

发布核心：在 velum-engine 里运行 `scripts/release-android.sh <版本>`，把打印的 version、sha256 写进 `android/engine.lock`。

## 发布签名

没有签名配置时，release 包使用调试签名，只适合自己测试。正式分发前生成一次密钥（妥善保管，丢失后无法给已安装的用户升级）：

```sh
keytool -genkeypair -v -keystore ~/velum-release.jks -alias velum -keyalg RSA -keysize 4096 -validity 36500
```

然后创建 `android/key.properties`（已在 .gitignore 中，不要提交）：

```properties
storeFile=/Users/you/velum-release.jks
storePassword=…
keyAlias=velum
keyPassword=…
```
