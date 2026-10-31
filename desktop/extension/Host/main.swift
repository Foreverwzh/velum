// 占位宿主：只用于让 Xcode 生成 Velum（dev.velum.desktop）的 Developer ID 描述文件与 entitlements，并导出签好名的扩展。
// 真正的宿主是 Tauri 打包的 Velum.app，build.sh 从这里取出扩展与描述文件交给 Tauri。
import Foundation

exit(0)
