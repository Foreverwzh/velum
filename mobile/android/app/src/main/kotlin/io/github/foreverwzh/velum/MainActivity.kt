package io.github.foreverwzh.velum

import android.Manifest
import android.content.Intent
import android.content.pm.ApplicationInfo
import android.content.pm.PackageManager
import android.graphics.Bitmap
import android.graphics.Canvas
import android.net.VpnService
import android.os.Build
import io.flutter.embedding.android.FlutterActivity
import io.flutter.embedding.engine.FlutterEngine
import io.flutter.plugin.common.MethodChannel
import java.io.ByteArrayOutputStream
import kotlin.concurrent.thread

/** Flutter 界面的宿主。界面经 UDS 直接访问核心；这里只提供 Go 做不到的平台操作（VPN 授权、通知权限、应用列表）。 */
class MainActivity : FlutterActivity() {
    companion object {
        private const val REQ_VPN = 1
        private const val REQ_NOTIFICATIONS = 2
    }

    private var pendingVpn: MethodChannel.Result? = null

    override fun configureFlutterEngine(flutterEngine: FlutterEngine) {
        super.configureFlutterEngine(flutterEngine)
        MethodChannel(flutterEngine.dartExecutor.binaryMessenger, "velum/native").setMethodCallHandler { call, result ->
            when (call.method) {
                // 核心状态：socket 路径、Velum 目录；启动失败时附带原因
                "core" -> {
                    Core.startAsync()
                    result.success(
                        mapOf("socket" to Core.socket, "home" to Core.home, "running" to Core.running, "error" to Core.startError),
                    )
                }
                // 已安装的应用（按应用分流用）：包名、名称、是否系统应用
                "apps" -> thread {
                    val pm = packageManager
                    val apps = pm.getInstalledApplications(0).filter { it.packageName != packageName }.map {
                        mapOf(
                            "package" to it.packageName,
                            "label" to pm.getApplicationLabel(it).toString(),
                            "system" to (it.flags and ApplicationInfo.FLAG_SYSTEM != 0),
                        )
                    }
                    runOnUiThread { result.success(apps) }
                }
                // 应用图标（PNG），列表滚动时按需加载
                "appIcon" -> thread {
                    val png = try {
                        val d = packageManager.getApplicationIcon(call.arguments as String)
                        val bmp = Bitmap.createBitmap(96, 96, Bitmap.Config.ARGB_8888)
                        d.setBounds(0, 0, 96, 96)
                        d.draw(Canvas(bmp))
                        ByteArrayOutputStream().also { bmp.compress(Bitmap.CompressFormat.PNG, 100, it) }.toByteArray()
                    } catch (_: Exception) {
                        null
                    }
                    runOnUiThread { result.success(png) }
                }
                // 申请 VPN 授权（首次会弹出系统确认框）；返回是否已授权
                "prepareVpn" -> {
                    val intent = VpnService.prepare(this)
                    if (intent == null) {
                        result.success(true)
                    } else {
                        pendingVpn?.success(false)
                        pendingVpn = result
                        @Suppress("DEPRECATION")
                        startActivityForResult(intent, REQ_VPN)
                    }
                }
                // Android 13+ 需要通知权限才能显示前台服务通知（不授权时 VPN 仍可工作）
                "requestNotifications" -> {
                    if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.TIRAMISU &&
                        checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) != PackageManager.PERMISSION_GRANTED
                    ) {
                        requestPermissions(arrayOf(Manifest.permission.POST_NOTIFICATIONS), REQ_NOTIFICATIONS)
                    }
                    result.success(null)
                }
                else -> result.notImplemented()
            }
        }
    }

    @Deprecated("Deprecated in Java")
    override fun onActivityResult(requestCode: Int, resultCode: Int, data: Intent?) {
        if (requestCode == REQ_VPN) {
            pendingVpn?.success(resultCode == RESULT_OK)
            pendingVpn = null
            return
        }
        @Suppress("DEPRECATION")
        super.onActivityResult(requestCode, resultCode, data)
    }
}
