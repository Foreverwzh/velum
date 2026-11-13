package io.github.foreverwzh.velum

import android.content.Context
import android.net.ConnectivityManager
import android.net.LinkProperties
import android.net.Network
import android.net.NetworkCapabilities
import android.net.NetworkRequest
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.util.Log
import org.json.JSONArray
import org.json.JSONObject
import java.io.File
import java.net.Inet4Address
import java.util.concurrent.atomic.AtomicBoolean
import kotlin.concurrent.thread

/**
 * 核心（Rust，见 [RustCore]）在 App 进程内的入口：启动核心，并实现核心回调的 [RustCore.Host]。
 * 业务逻辑全部在核心里；界面经 HTTP over UDS（[socket]）访问核心，与桌面端相同。
 */
object Core : RustCore.Host {
    private const val TAG = "Velum"

    private lateinit var app: Context
    private val starting = AtomicBoolean(false)

    @Volatile var running = false
        private set
    @Volatile var startError: String? = null
        private set

    // 当前物理网络（非 VPN）
    @Volatile private var networkJSON = ""

    val home: String get() = File(app.filesDir, "velum").absolutePath
    val socket: String get() = File(home, "velumd.sock").absolutePath

    fun init(ctx: Context) {
        app = ctx.applicationContext
        trackNetwork()
    }

    /** 在后台线程启动核心（Start 会等待本地接口就绪，且可能回调 openTun，不能占用主线程）。 */
    fun startAsync() {
        if (running || !starting.compareAndSet(false, true)) return
        thread(name = "velum-core") {
            try {
                val err = RustCore.start(
                    home, socket, java.util.Locale.getDefault().toLanguageTag(), translations(), version(), this,
                )
                if (err != null) throw IllegalStateException(err)
                running = true
                startError = null
            } catch (e: Exception) {
                Log.e(TAG, "启动核心失败", e)
                startError = e.message ?: e.toString()
            } finally {
                starting.set(false)
            }
        }
    }

    /**
     * 核心文字的英文表（assets/core-en.json，构建时由 scripts/core.sh 生成）：核心要的是文件路径，复制到 App 目录。
     * 中文环境不需要（文字的原文就是中文）。
     */
    private fun translations(): String? {
        if (java.util.Locale.getDefault().language == "zh") return null
        return try {
            val f = File(app.filesDir, "core-en.json")
            app.assets.open("core-en.json").use { input -> f.outputStream().use { input.copyTo(it) } }
            f.absolutePath
        } catch (e: Exception) {
            Log.w(TAG, "没有核心的英文表", e)
            null
        }
    }

    private fun version(): String =
        app.packageManager.getPackageInfo(app.packageName, 0).versionName ?: ""

    override fun openTun(req: String): Int = VelumVpnService.open(app, JSONObject(req))

    override fun closeTun() = VelumVpnService.close()

    override fun network(): String = networkJSON

    /**
     * 跟踪物理网络（排除 VPN）。不能用默认网络回调：Velum 虽已从 VPN 中排除，回调仍会报告 VPN 网络，
     * 核心会把它当成外部全局 VPN 而暂停 TUN。
     */
    private fun trackNetwork() {
        val cm = app.getSystemService(ConnectivityManager::class.java)
        val req = NetworkRequest.Builder()
            .addCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET)
            .addCapability(NetworkCapabilities.NET_CAPABILITY_NOT_VPN)
            .build()
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.S) {
            // 系统按默认网络的规则挑选最佳的非 VPN 网络
            cm.registerBestMatchingNetworkCallback(req, object : ConnectivityManager.NetworkCallback() {
                private var current: Network? = null
                override fun onLinkPropertiesChanged(n: Network, lp: LinkProperties) {
                    current = n
                    networkJSON = describe(lp)
                }
                override fun onLost(n: Network) {
                    if (n == current) {
                        current = null
                        networkJSON = ""
                    }
                }
            }, Handler(Looper.getMainLooper()))
            return
        }
        // Android 10 / 11：跟踪所有非 VPN 网络，取系统当前的活动网络，否则取最近可用的一个
        val nets = LinkedHashMap<Network, LinkProperties>()
        fun pick() {
            val lp = cm.activeNetwork?.let { nets[it] } ?: nets.values.lastOrNull()
            networkJSON = lp?.let { describe(it) } ?: ""
        }
        cm.registerNetworkCallback(req, object : ConnectivityManager.NetworkCallback() {
            override fun onLinkPropertiesChanged(n: Network, lp: LinkProperties) {
                synchronized(nets) { nets.remove(n); nets[n] = lp; pick() }
            }
            override fun onLost(n: Network) {
                synchronized(nets) { nets.remove(n); pick() }
            }
        })
    }

    /** 转换为核心要的物理网络 JSON（velum-daemon 的 platform::HostNetwork）。IPv4 DNS 排在前面（核心取第一个作为系统 DNS）。 */
    private fun describe(lp: LinkProperties): String {
        val dns = lp.dnsServers.sortedBy { if (it is Inet4Address) 0 else 1 }.mapNotNull { it.hostAddress }
        val gw = lp.routes.firstOrNull { it.isDefaultRoute && it.gateway is Inet4Address }?.gateway?.hostAddress
        return JSONObject()
            .put("iface", lp.interfaceName ?: "")
            .put("gateway", gw ?: "")
            .put("dns", JSONArray(dns))
            .toString()
    }
}
