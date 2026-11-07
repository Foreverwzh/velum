# 在 Linux 服务器上部署 Velum

需要 systemd。实际验证过 Ubuntu 24.04 和 Debian 13（x86_64）；arm64 只跑过自动化测试；Ubuntu 22.04、Debian 12 在支持范围内，但还没有实测。其他 systemd 发行版能跑但不保证。

Velum 以专用用户 `velum` 运行，不需要 root。配置在 `/etc/velum`，数据在 `/var/lib/velum`，日志用 `journalctl -u velum` 查看。

## 安装

从 [Releases](https://github.com/Foreverwzh/velum/releases) 下载对应版本的 `velum-install.sh`（目前都是预发布版本，所以没有固定的“最新版”地址），然后：

```sh
sudo sh velum-install.sh                    # 下载最新版本（含预发布版本）并校验
sudo vlu sub '<订阅地址>'
```

`--version <版本>` 可以指定版本。首次安装只校验 `SHA256SUMS`，不验证签名，因为验证签名的程序就在压缩包里；之后的 `vlu upgrade` 都会验证。

无法访问 GitHub 时，把 `Velum-<版本>-linux-<架构>.tar.gz` 和 `SHA256SUMS` 传到服务器的同一目录，再运行 `sudo sh velum-install.sh <压缩包>`。

安装时会把执行 `sudo` 的用户加入 `velum` 组，重新登录后可以不加 `sudo` 使用 `vlu`。**组成员能经 `vlu` 查看和修改配置，包括订阅地址。**

装好后只有本地端口工作，不会改动这台机器的网络。

## 网页界面

用浏览器切换节点、看连接、改订阅和本地规则。默认关闭，在 `/etc/velum/config.yaml` 里打开：

```yaml
web:
  enabled: true
  listen: 127.0.0.1:9090
  password: "换成一个足够长的密码"
```

然后执行 `sudo systemctl restart velum`，`vlu status` 会显示地址。登录时用户名随意填。

默认只听本机，用 SSH 隧道访问：

```sh
ssh -L 9090:127.0.0.1:9090 <服务器>   # 然后打开 http://127.0.0.1:9090
```

要从局域网直接访问，把 `listen` 改为 `0.0.0.0:9090`。**界面没有 HTTPS，密码以明文传输**，同一网络里的设备有可能截获，不要直接暴露到公网；确实需要时放在 Caddy 等反向代理后面。

通过 HTTP 访问时，浏览器可能在输入密码后弹出安全警告，要确认后页面才会继续加载。如果页面一直停在加载状态，先看看有没有未处理的警告。

能登录的人可以查看订阅地址、修改配置，所以密码要和 SSH 一样对待。网页界面不能开启 TUN，也不能停止 Velum，以免把自己断开；这两个操作要回到服务器上执行。

## 用法一：只用本地端口

适合只想让部分程序走代理的情况。

```sh
eval "$(vlu env)"      # 让当前 shell 的命令行程序走 Velum
curl https://example.com
```

需要长期生效的程序单独配置，端口默认是 `127.0.0.1:7899`：

```sh
# apt
echo 'Acquire::http::Proxy "http://127.0.0.1:7899";' | sudo tee /etc/apt/apt.conf.d/90velum

# Docker 拉取镜像
sudo mkdir -p /etc/systemd/system/docker.service.d
sudo tee /etc/systemd/system/docker.service.d/proxy.conf <<'CONF'
[Service]
Environment=HTTP_PROXY=http://127.0.0.1:7899
Environment=HTTPS_PROXY=http://127.0.0.1:7899
CONF
sudo systemctl daemon-reload && sudo systemctl restart docker
```

要提供给其他机器时，`listen` 不能改成对外地址（没有认证，就是开放代理，服务会拒绝）。在 `/etc/velum/config.yaml` 里配置带认证的入口，然后 `sudo vlu reload`：

```yaml
listen_public:
  address: 0.0.0.0:7898
  username: <用户名>
  password: <密码>
```

## 用法二：全局接管（TUN）

本机和容器的流量都按规则走。为了不把自己锁在服务器外面，要先试运行：

```sh
sudo vlu tun on --try        # 120 秒内不确认就自动回退，重启后也会回退
```

然后**新开一个 SSH 连接**确认。能连上，才说明新的入站连接没有受影响：

```sh
sudo vlu tun confirm
```

退出接管用 `sudo vlu tun off`。

默认不进隧道的网段：`10.0.0.0/8`、`172.16.0.0/12`、`192.168.0.0/16`、`169.254.0.0/16`、`fc00::/7`、`fe80::/10`，所以云元数据和同 VPC 的内网不受影响。要再排除其他网段，修改 `config.yaml` 的 `tun.route_exclude`。

## 用法三：和 Docker 一起用

开启 TUN 后，容器的出站流量也会按规则走，端口映射不受影响。

如果开了 **ufw**，容器会无法出站：容器的连接被转到本机的随机高位端口，而 ufw 默认拒绝入站。这要放宽防火墙，所以 Velum 只提示，不会替你修改：

```sh
sudo ufw allow from 172.16.0.0/12 proto tcp to any port 32768:60999
```

这条规则允许容器访问本机的高位 TCP 端口。Docker 网络不在 `172.16.0.0/12` 内时，要改成实际的网段。放行之后，`vlu warnings` 里的提示仍会显示，因为 Velum 读不到 ufw 的规则文件。

firewalld 默认配置下不需要处理。

## 和其他 VPN 一起用

- **WireGuard 分流、覆盖网络（如 Tailscale）**：自动识别，它们的网段和对端地址不进隧道。已用真实的 WireGuard 握手和模拟的覆盖网络验证，真实的 Tailscale 还没有测。
- **全局型 VPN（如 `AllowedIPs = 0.0.0.0/0`）**：Velum 优先，它不再是出口。
- **同机运行的其他 sing-box**：如果也开了 `auto_redirect`，两边会互相影响，不要同时使用。

## 升级

```sh
vlu status              # 有新版本时会提示
sudo vlu upgrade        # --beta 包括预发布版本，--version 指定版本
```

升级前会校验发布签名和校验和，服务会重启，连接会短暂中断。新版本没有正常运行时，会自动恢复旧版本。配置不变。

不能访问 GitHub 时，会自动改经 Velum 的本地端口下载。也可以手动下载 `Velum-<版本>-linux-<架构>.tar.gz`、`SHA256SUMS`、`SHA256SUMS.sig`，放在同一目录后执行 `sudo vlu upgrade --file <压缩包>`。

## 卸载

```sh
sudo velumd service uninstall           # 保留配置和数据
sudo velumd service uninstall --purge   # 连同配置、数据和 velum 用户一起删除
```

## 排查

| 现象 | 处理 |
| :--- | :--- |
| `vlu` 提示没有权限 | 把用户加入 `velum` 组后重新登录，或用 `sudo vlu` |
| 接管后连不上服务器 | 试运行期间等它自动回退，或重启机器；都会回到端口模式 |
| 接管后域名解析异常 | `vlu why <域名>` 看解析走哪个 DNS。只有本机解析器（如 dnsmasq）时，Velum 拿不到上游 DNS，`vlu warnings` 会提示 |
| 想知道某个连接为什么这样走 | `vlu why <网址>`，加 `--live` 看实际命中的规则 |
