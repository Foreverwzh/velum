package io.github.foreverwzh.velum

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.content.pm.ServiceInfo
import android.net.VpnService
import android.os.Build
import io.github.foreverwzh.velum.core.mobilebind.Mobilebind
import org.json.JSONArray
import org.json.JSONObject
import java.util.concurrent.CompletableFuture
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicReference

/**
 * 建立 VPN 的平台层（对应 macOS 的 velum-helper）：由 Go 核心经 [Core.openTun] 调用，
 * 按编译器给出的地址、路由、DNS 建立 VPN，把 TUN 的 FD 交给核心。服务以前台服务运行，保持进程存活。
 */
class VelumVpnService : VpnService() {
    companion object {
        private const val CHANNEL = "velum"
        private const val NOTIFICATION_ID = 1
        private const val ACTION_START = "io.github.foreverwzh.velum.START"

        @Volatile private var instance: VelumVpnService? = null
        private val created = AtomicReference(CompletableFuture<VelumVpnService>())

        /** 建立 VPN，返回 TUN 的 FD（所有权交给调用方）。在核心的线程上调用。 */
        fun open(ctx: Context, req: JSONObject): Int {
            if (prepare(ctx) != null) {
                throw IllegalStateException("尚未授权 Velum 建立 VPN，请在 App 中开启 TUN")
            }
            val svc = instance ?: run {
                val pending = created.get()
                ctx.startForegroundService(Intent(ctx, VelumVpnService::class.java).setAction(ACTION_START))
                pending.get(5, TimeUnit.SECONDS)
            }
            return svc.establish(req)
        }

        /** 关闭 VPN（FD 已由核心关闭）。 */
        fun close() {
            instance?.shutdown()
        }
    }

    override fun onCreate() {
        super.onCreate()
        startForeground()
        instance = this
        created.get().complete(this)
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        startForeground()
        if (intent?.action != ACTION_START) {
            // 由系统直接拉起（「始终开启的 VPN」、开机自动连接）：启动核心并请求开启 TUN，核心随后经 open 建立 VPN
            Core.startAsync()
            Mobilebind.tunRequested()
        }
        return START_NOT_STICKY
    }

    override fun onDestroy() {
        instance = null
        created.set(CompletableFuture())
        super.onDestroy()
    }

    /** 用户在系统设置中断开，或被其他 VPN 取代：通知核心关闭 TUN 开关，核心随后调用 close。 */
    override fun onRevoke() {
        if (Core.running) Mobilebind.tunRevoked() else shutdown()
    }

    private fun establish(req: JSONObject): Int {
        val b = Builder().setSession("Velum").setMtu(req.optInt("mtu", 9000))
        for (a in strings(req, "inet4") + strings(req, "inet6")) {
            val (addr, len) = cidr(a)
            b.addAddress(addr, len)
        }
        for (r in strings(req, "routes")) {
            val (addr, len) = cidr(r)
            b.addRoute(addr, len)
        }
        strings(req, "dns").forEach { b.addDnsServer(it) }
        // 核心的出站连接（含 DNS）不经 VPN，直接走物理网络，避免回环。设置了 include_apps 时只有列出的应用经 VPN，
        // Velum 自身不在其中，同样不经 VPN（两种方式不能混用）
        val include = strings(req, "include_apps")
        if (include.isNotEmpty()) {
            include.filter { it != packageName }.forEach { app -> ignoreMissing { b.addAllowedApplication(app) } }
        } else {
            b.addDisallowedApplication(packageName)
            strings(req, "exclude_apps").forEach { app -> ignoreMissing { b.addDisallowedApplication(app) } }
        }
        b.setMetered(false)
        val pfd = b.establish() ?: throw IllegalStateException("系统拒绝建立 VPN（可能未授权）")
        return pfd.detachFd()
    }

    private fun shutdown() {
        stopForeground(STOP_FOREGROUND_REMOVE)
        stopSelf()
    }

    private fun startForeground() {
        val nm = getSystemService(NotificationManager::class.java)
        nm.createNotificationChannel(NotificationChannel(CHANNEL, "Velum 运行状态", NotificationManager.IMPORTANCE_LOW))
        val open = PendingIntent.getActivity(
            this, 0, Intent(this, MainActivity::class.java), PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
        )
        val n = Notification.Builder(this, CHANNEL)
            .setSmallIcon(R.drawable.ic_stat_velum)
            .setContentTitle("Velum 正在保护")
            .setContentText("所有应用的流量经 Velum 分流")
            .setContentIntent(open)
            .setOngoing(true)
            .build()
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.UPSIDE_DOWN_CAKE) {
            startForeground(NOTIFICATION_ID, n, ServiceInfo.FOREGROUND_SERVICE_TYPE_SYSTEM_EXEMPTED)
        } else {
            startForeground(NOTIFICATION_ID, n)
        }
    }

    /** 配置中的应用未安装时跳过（不影响建立 VPN）。 */
    private inline fun ignoreMissing(f: () -> Unit) {
        try {
            f()
        } catch (_: PackageManager.NameNotFoundException) {
        }
    }

    private fun strings(o: JSONObject, key: String): List<String> {
        val a: JSONArray = o.optJSONArray(key) ?: return emptyList()
        return (0 until a.length()).map { a.getString(it) }
    }

    private fun cidr(s: String): Pair<String, Int> {
        val i = s.lastIndexOf('/')
        require(i > 0) { "地址格式错误：$s" }
        return s.substring(0, i) to s.substring(i + 1).toInt()
    }
}
