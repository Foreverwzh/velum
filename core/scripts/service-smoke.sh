#!/bin/sh
# 系统服务的端到端测试：安装、权限、对外监听、崩溃拉起、升级、卸载。需要 root、systemd，**会安装和删除 velum 服务**，只在测试机上运行。
# 用法：sudo sh service-smoke.sh <含 velumd、vlu、install.sh 的目录>
set -eu
dir="$(cd "$1" && pwd)"
[ "$(id -u)" = 0 ] || { echo "需要 root" >&2; exit 1; }
[ ! -e /etc/systemd/system/velum.service ] || { echo "已安装 velum 服务，不覆盖" >&2; exit 1; }
if id velum >/dev/null 2>&1; then echo "已有 velum 用户，无法测试专用用户" >&2; exit 1; fi
unset SUDO_USER http_proxy https_proxy all_proxy HTTP_PROXY HTTPS_PROXY ALL_PROXY no_proxy NO_PROXY
port=17899 pub=17900 web=17898 member=velumsmoke
www="$(mktemp -d)"; chmod 755 "$www"
srv=""
fail() { echo "service-smoke: 失败：$*" >&2; exit 1; }
cleanup() {
  status=$?
  if [ "$status" -ne 0 ]; then
    systemctl status --no-pager velum >&2 2>&1 || true
    journalctl -u velum --no-pager -n 30 >&2 2>&1 || true
  fi
  [ -z "$srv" ] || kill "$srv" 2>/dev/null || true
  /usr/local/bin/velumd service uninstall --purge >/dev/null 2>&1 || true
  id "$member" >/dev/null 2>&1 && userdel "$member" || true
  rm -rf "$www"
  exit "$status"
}
trap cleanup EXIT INT TERM

until_true() { # 最多等 $1 秒
  n=$(( $1 * 5 )); shift
  while ! "$@" >/dev/null 2>&1; do
    n=$((n - 1)); [ "$n" -gt 0 ] || return 1
    sleep 0.2
  done
}
ready() { vlu status | grep -q "$port"; }
main_pid() { systemctl show -p MainPID --value velum; }

useradd "$member"
echo velum-smoke-ok >"$www/index.html"
(cd "$www" && exec python3 -m http.server "$web" --bind 127.0.0.1 >/dev/null 2>&1) &
srv=$!

sh "$dir/install.sh" >/dev/null
systemctl is-active -q velum || fail "安装后服务没有运行"
systemctl is-enabled -q velum || fail "没有设为开机启动"
[ "$(stat -c %a:%U /etc/velum)" = 700:velum ] || fail "/etc/velum 权限不对"
[ "$(stat -c %a:%U /var/lib/velum)" = 700:velum ] || fail "/var/lib/velum 权限不对"
until_true 10 test -S /run/velum/velumd.sock || fail "没有本地接口"
[ "$(stat -c %a:%U:%G /run/velum/velumd.sock)" = 660:velum:velum ] || fail "socket 权限不对：$(stat -c %a:%U:%G /run/velum/velumd.sock)"
pid="$(main_pid)"
[ "$(stat -c %U "/proc/$pid")" = velum ] || fail "不是以 velum 用户运行"
[ "$(awk '/^CapAmb/ {print $2}' "/proc/$pid/status")" = 0000000000003400 ] || fail "capabilities 不符：$(grep Cap "/proc/$pid/status")"
echo "service-smoke: 安装、目录、权限正确"

# 接本地订阅
cat >/etc/velum/sub.yaml <<YAML
proxies:
  - {name: unused, type: ss, server: 192.0.2.1, port: 8388, cipher: aes-128-gcm, password: smoke}
proxy-groups:
  - {name: PROXY, type: select, proxies: [unused, DIRECT]}
rules:
  - MATCH,DIRECT
YAML
write_config() {
  cat >/etc/velum/config.yaml <<YAML
listen: $1
$2
log_level: warn
subscription:
  file: sub.yaml
YAML
  chown velum:velum /etc/velum/sub.yaml /etc/velum/config.yaml
  chmod 600 /etc/velum/sub.yaml /etc/velum/config.yaml
}
write_config "127.0.0.1:$port" ""
systemctl restart velum
until_true 30 ready || { vlu status >&2 || true; fail "加载配置失败"; }
[ "$(curl -fsS --max-time 10 --proxy "http://127.0.0.1:$port" "http://127.0.0.1:$web/")" = velum-smoke-ok ] || fail "经本地端口访问失败"
echo "service-smoke: 本地端口可用"

# Linux 没有系统代理后端：不能显示为已开启
vlu sysproxy on >/dev/null 2>&1 && fail "vlu sysproxy on 不应成功"
if vlu status | grep -qE '系统代理 开|system proxy on'; then fail "状态误报系统代理已开启"; fi
vlu sysproxy config >/dev/null 2>&1 || true
echo "service-smoke: 系统代理如实显示不支持"

# 访问控制
home="$(mktemp -d)"; chmod 777 "$home"
out="$(sudo -u nobody env HOME="$home" /usr/local/bin/vlu status 2>&1 || true)"
echo "$out" | grep -qE 'velum 组|velum group' || fail "未加入 velum 组的用户没有被拒绝：$out"
usermod -aG velum "$member"
sudo -u "$member" env HOME="$home" /usr/local/bin/vlu status | grep -q "$port" || fail "velum 组成员无法使用 vlu"
rm -rf "$home"
echo "service-smoke: socket 只对 velum 组开放"

# 对外监听
write_config "0.0.0.0:$port" ""
systemctl restart velum
until_true 15 sh -c 'vlu status 2>&1 | grep -qE "开放代理|open proxy"' || { vlu status >&2 || true; fail "无认证的 listen 没有被拒绝"; }
if ss -Hltn "sport = :$port" | grep -q .; then fail "被拒绝后端口仍在监听"; fi
write_config "127.0.0.1:$port" "listen_public: {address: 127.0.0.1:$pub, username: \"\", password: \"\"}"
systemctl restart velum
until_true 15 sh -c 'vlu status 2>&1 | grep -q listen_public' || { vlu status >&2 || true; fail "没有密码的 listen_public 没有被拒绝"; }
write_config "127.0.0.1:$port" "listen_public: {address: 127.0.0.1:$pub, username: smoke, password: secret}"
systemctl restart velum
until_true 30 ready || fail "带认证的 listen_public 启动失败"
code="$(curl -s -o /dev/null -w '%{http_code}' --max-time 10 --proxy "http://127.0.0.1:$pub" "http://127.0.0.1:$web/" || true)"
[ "$code" = 407 ] || fail "对外入口没有要求认证：$code"
[ "$(curl -fsS --max-time 10 --proxy "http://smoke:secret@127.0.0.1:$pub" "http://127.0.0.1:$web/")" = velum-smoke-ok ] || fail "带认证访问对外入口失败"
echo "service-smoke: 对外监听受控"

# 崩溃拉起
old="$(main_pid)"
kill -9 "$old"
new_pid() { p="$(main_pid)"; [ "$p" != 0 ] && [ "$p" != "$old" ] && systemctl is-active -q velum; }
until_true 15 new_pid || fail "kill -9 后没有被拉起"
until_true 30 ready || fail "拉起后没有恢复"
echo "service-smoke: kill -9 后自动拉起"

# vlu down / up
vlu down >/dev/null
until_true 10 sh -c '! systemctl is-active -q velum' || fail "vlu down 后服务还在运行"
vlu up >/dev/null || fail "vlu up 失败"
systemctl is-active -q velum || fail "vlu up 没有经 systemd 启动"
echo "service-smoke: vlu up / down 经 systemd"

# 升级：配置保留
sum="$(sha256sum /etc/velum/config.yaml)"
sh "$dir/install.sh" >/dev/null
systemctl is-active -q velum || fail "升级后服务没有运行"
[ "$(sha256sum /etc/velum/config.yaml)" = "$sum" ] || fail "升级改动了配置"
until_true 30 ready || fail "升级后没有恢复"
echo "service-smoke: 重复安装（升级）保留配置"

# 卸载
/usr/local/bin/velumd service uninstall >/dev/null
[ ! -e /etc/systemd/system/velum.service ] && [ ! -e /usr/local/bin/velumd ] || fail "卸载后有残留"
[ -f /etc/velum/config.yaml ] || fail "普通卸载不应删除配置"
sh "$dir/install.sh" >/dev/null
/usr/local/bin/velumd service uninstall --purge >/dev/null
[ ! -e /etc/velum ] && [ ! -e /var/lib/velum ] || fail "--purge 后有残留"
if id velum >/dev/null 2>&1; then fail "--purge 后 velum 用户还在"; fi
if getent group velum >/dev/null; then fail "--purge 后 velum 组还在（组里有其他成员时）"; fi
echo "service-smoke: 卸载干净"

# 已有同名的真人账号：不能借用，也不能被删除
useradd -m velum
if out="$(sh "$dir/install.sh" 2>&1)"; then fail "已有同名用户时不应安装成功"; fi
echo "$out" | grep -qE '不是 Velum 创建的|not created by Velum' || fail "同名用户的提示不对：$out"
[ ! -e /etc/systemd/system/velum.service ] && [ ! -e /usr/local/bin/velumd ] || fail "拒绝安装后留下了文件"
"$dir/velumd" service uninstall --purge >/dev/null || fail "uninstall --purge 失败"
id velum >/dev/null 2>&1 || fail "--purge 删除了不是 Velum 创建的用户"
userdel -r velum 2>/dev/null
echo "service-smoke: 不借用、不删除同名的已有账号"
echo "service-smoke: 通过"
