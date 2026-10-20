# Velum 移动端（Flutter，目前只有 Android）

设计与进度见 `docs/M3-PLAN.md`。Android 10（API 29）及以上。

## 构建

需要：Go、Android SDK（含 NDK）、JDK 17+、Flutter，以及与 `core/go.mod` 中 `golang.org/x/mobile` 同版本的 gomobile：

```sh
cd core && v=$(go list -m -f '{{.Version}}' golang.org/x/mobile) \
  && go install golang.org/x/mobile/cmd/gomobile@$v golang.org/x/mobile/cmd/gobind@$v
```

```sh
export ANDROID_HOME=…                          # Android SDK
sh scripts/core.sh                             # Go 核心 → android/app/libs/velum.aar（32 / 64 位 ARM、x86_64）
flutter build apk --release --split-per-abi   # 每个架构一个 APK（约 24MB）；手机一般用 arm64-v8a
```

开发调试：`TARGETS=android/arm64 sh scripts/core.sh` 只编译一个架构，更快。

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
