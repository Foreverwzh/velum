package io.github.foreverwzh.velum

import android.app.Application

/** 进程启动即启动核心（界面打开、系统拉起 VPN 服务都会经过这里）。 */
class VelumApp : Application() {
    override fun onCreate() {
        super.onCreate()
        Core.init(this)
        Core.startAsync()
    }
}
