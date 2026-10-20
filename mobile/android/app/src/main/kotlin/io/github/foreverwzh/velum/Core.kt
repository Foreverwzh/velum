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
import android.os.Process
import android.util.Log
import io.github.foreverwzh.velum.core.mobilebind.Host
import io.github.foreverwzh.velum.core.mobilebind.Mobilebind
import org.json.JSONArray
import org.json.JSONObject
import java.io.File
import java.net.Inet4Address
import java.net.InetAddress
import java.net.InetSocketAddress
import java.util.concurrent.atomic.AtomicBoolean
import kotlin.concurrent.thread

/**
 * Go 核心（daemon）在 App 进程内的入口：启动核心，并实现 Go 回调的 Host 接口。
 * 业务逻辑全部在 Go 中；界面经 HTTP over UDS（[socket]）访问核心，与桌面端相同。
 */
object Core : Host {
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
    val socket: String get() = Mobilebind.socket(home)

    fun init(ctx: Context) {
        app = ctx.applicationContext
        trackNetwork()
    }

    /** 在后台线程启动核心（Start 会等待本地接口就绪，且可能回调 openTun，不能占用主线程）。 */
    fun startAsync() {
        if (running || !starting.compareAndSet(false, true)) return
        thread(name = "velum-core") {
            try {
                Mobilebind.start(home, this)
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

    override fun openTun(req: String): Int = VelumVpnService.open(app, JSONObject(req))

    override fun closeTun() = VelumVpnService.close()

    override fun network(): String = networkJSON

    // uid → 包名（应用安装 / 卸载很少发生，进程内缓存即可；查不到的 uid 不缓存）
    private val packages = android.util.LruCache<Int, List<String>>(256)

    /** 查询 VPN 中一条连接所属的应用（只有当前生效的 VPN 应用可以调用 getConnectionOwnerUid）。 */
    override fun findOwner(protocol: Int, srcAddr: String, srcPort: Int, dstAddr: String, dstPort: Int): String {
        val cm = app.getSystemService(ConnectivityManager::class.java)
        val uid = try {
            cm.getConnectionOwnerUid(
                protocol,
                InetSocketAddress(InetAddress.getByName(srcAddr), srcPort),
                InetSocketAddress(InetAddress.getByName(dstAddr), dstPort),
            )
        } catch (e: SecurityException) {
            return "" // VPN 刚关闭 / 尚未生效
        }
        if (uid == Process.INVALID_UID) return ""
        val pkgs = packages.get(uid) ?: ownerPackages(uid).also { if (it.isNotEmpty()) packages.put(uid, it) }
        return JSONObject().put("uid", uid).put("packages", JSONArray(pkgs)).toString()
    }

    /**
     * uid 对应的包名。共享 uid（如系统 uid 1000 下有上百个包）时把 uid 名称（如 android.uid.system:1000）放在最前，
     * 核心以第一个作为连接的进程名，避免显示成其中任意一个包。
     */
    private fun ownerPackages(uid: Int): List<String> {
        val pm = app.packageManager
        val pkgs = pm.getPackagesForUid(uid)?.toList() ?: emptyList()
        if (pkgs.size <= 1) return pkgs
        return listOfNotNull(pm.getNameForUid(uid)) + pkgs
    }

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

    /** 转换为 mobilebind.Network 的 JSON。IPv4 DNS 排在前面（核心取第一个作为系统 DNS）。 */
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
