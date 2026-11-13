package io.github.foreverwzh.velum

/**
 * Rust 核心（velum-engine 的 velum-android：与桌面 velumd 相同的 daemon）的 JNI 入口，库为 libvelum_android.so。
 * 界面经 Unix socket 上的 HTTP 访问核心（接口与桌面相同）；建立 VPN、报告物理网络由 [Host] 提供。
 */
object RustCore {
    init {
        System.loadLibrary("velum_android")
    }

    /** 核心回调 App 的平台能力。在核心的线程上调用，可以阻塞。 */
    interface Host {
        /** 按请求（JSON：mtu、inet4、inet6、routes、dns、include_apps、exclude_apps）建立 VPN，返回 TUN 的 FD（所有权交给核心）。失败时抛异常。 */
        fun openTun(req: String): Int

        /** 关闭 VPN（FD 已由核心关闭）。 */
        fun closeTun()

        /** 当前物理网络（JSON：iface、gateway、dns），没有时为空字符串。 */
        fun network(): String
    }

    /** 启动核心，本地接口就绪后返回；失败时返回原因，成功为 null。已在运行时直接返回。会阻塞。 */
    external fun start(home: String, socket: String, lang: String, translations: String?, version: String, host: Host): String?

    /** 停止核心（关闭 VPN）。 */
    external fun stop()

    /** 系统直接拉起 VPN 服务（「始终开启的 VPN」、开机自动连接）：打开 TUN 开关。异步，立即返回。 */
    external fun tunRequested()

    /** 系统撤销了 VPN：关闭 TUN 开关，与界面上关闭等效。异步，立即返回。 */
    external fun tunRevoked()
}
