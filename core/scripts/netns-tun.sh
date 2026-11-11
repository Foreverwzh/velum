#!/bin/bash
# TUN 回归测试：用真实的 velumd 走一遍 Linux 全局接管，全部在 netns 里，不影响运行它的机器的网络。
#   cli 198.51.100.2 ── r（路由器，也是「互联网」192.0.2.1、内网 10.9.9.9）── srv 203.0.113.10（velumd 跑在这里）
# 用法：sudo bash netns-tun.sh <velumd 与 vlu 所在目录>
# 同样的拓扑也有 IPv6：cli 2001:db8:1::2 ── r（「互联网」2001:db8:9::1）── srv 2001:db8:2::10
# 覆盖：对外服务不受接管影响（TCP 长连接、新连接、通配地址的 UDP，IPv4 和 IPv6）、默认排除网段、没有对外的监听端口、
# kill -9 后的清理、规则不累积、不动别人的规则。
# 不覆盖：回退定时器（依赖 systemd，在虚拟机上验证）、真实节点。
set -u
export VELUM_LANG=zh # 下面的断言匹配中文输出
bin="$(cd "$1" && pwd)"
[ "$(id -u)" = 0 ] || { echo "需要 root"; exit 1; }
for d in /run/velum /var/lib/velum; do
  [ ! -e "$d" ] || { echo "$d 已存在：这台机器可能装了 Velum，不在这里运行"; exit 1; }
done
work="$(mktemp -d)"
export VELUM_HOME="$work"
unset http_proxy https_proxy all_proxy HTTP_PROXY HTTPS_PROXY ALL_PROXY no_proxy NO_PROXY
S="ip netns exec vlsrv"; C="ip netns exec vlcli"; R="ip netns exec vlr"
vlu="$bin/vlu"
pids=()
velumd_pid=""
failed=0

cleanup() {
  status=$?
  [ -z "$velumd_pid" ] || kill -9 "$velumd_pid" 2>/dev/null
  for p in "${pids[@]:-}"; do [ -n "$p" ] && kill -9 "$p" 2>/dev/null; done
  for n in srv cli r; do ip netns del "vl$n" 2>/dev/null; done
  if [ "$failed" != 0 ] && [ -f "$work/velumd.out" ]; then
    echo "--- velumd 输出" >&2
    tail -30 "$work/velumd.out" >&2
  fi
  rm -rf "$work" /run/velum /var/lib/velum
  exit "$status"
}
trap cleanup EXIT INT TERM

check() { # $1 说明，$2 实际，$3 期望
  if [ "$2" = "$3" ]; then
    echo "  ✓ $1"
  else
    echo "  ✗ $1：得到「$2」，应为「$3」"
    failed=1
  fi
}

for n in srv cli r; do ip netns add "vl$n"; ip netns exec "vl$n" ip link set lo up; done
ip link add vs0 netns vlsrv type veth peer name vs1 netns vlr
ip link add vc0 netns vlcli type veth peer name vc1 netns vlr
$S ip addr add 203.0.113.10/24 dev vs0; $S ip link set vs0 up; $S ip route add default via 203.0.113.1
$C ip addr add 198.51.100.2/24 dev vc0; $C ip link set vc0 up; $C ip route add default via 198.51.100.1
$R ip addr add 203.0.113.1/24 dev vs1; $R ip link set vs1 up
$R ip addr add 198.51.100.1/24 dev vc1; $R ip link set vc1 up
$R ip addr add 192.0.2.1/32 dev lo
$R ip addr add 10.9.9.9/32 dev lo
$R sysctl -qw net.ipv4.ip_forward=1
$R sysctl -qw net.ipv6.conf.all.forwarding=1
$S ip -6 addr add 2001:db8:2::10/64 dev vs0 nodad; $S ip -6 route add default via 2001:db8:2::1
$C ip -6 addr add 2001:db8:1::2/64 dev vc0 nodad; $C ip -6 route add default via 2001:db8:1::1
$R ip -6 addr add 2001:db8:2::1/64 dev vs1 nodad; $R ip -6 addr add 2001:db8:1::1/64 dev vc1 nodad
$R ip -6 addr add 2001:db8:9::1/128 dev lo nodad

mkdir -p "$work/www"; echo internet-ok >"$work/www/index.html"
(cd "$work/www" && exec $R python3 -m http.server 80 --bind :: >/dev/null 2>&1) & pids+=($!)
cat >"$work/echo.py" <<'PY'
import socket,threading
s=socket.socket(); s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1); s.bind(("0.0.0.0",2222)); s.listen(16)
def h(c):
    while True:
        d=c.recv(100)
        if not d: return
        c.sendall(d)
while True:
    c,_=s.accept(); threading.Thread(target=h,args=(c,),daemon=True).start()
PY
$S python3 "$work/echo.py" & pids+=($!)
# 绑定通配地址的 UDP 服务（相当于 WireGuard、DNS 服务）：回包没有绑定源地址
cat >"$work/udp.py" <<'PY'
import socket
s=socket.socket(socket.AF_INET,socket.SOCK_DGRAM); s.bind(("0.0.0.0",5353))
while True:
    d,a=s.recvfrom(100); s.sendto(d,a)
PY
$S python3 "$work/udp.py" & pids+=($!)
# 服务端先发包的连接（相当于正在输出的 SSH）：接管后第一个包由本机发出。
# 间隔不能短：客户端的延迟 ACK 约 40 毫秒后回来，发得太频繁时接管后的第一个包多半是这个 ACK，就测不出问题
cat >"$work/push.py" <<'PY'
import socket,time
s=socket.socket(); s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1); s.bind(("0.0.0.0",2224)); s.listen(4)
c,_=s.accept()
while True:
    c.sendall(b"p"); time.sleep(1)
PY
$S python3 "$work/push.py" & pids+=($!)
cat >"$work/pushc.py" <<'PY'
import socket,sys
c=socket.create_connection(("203.0.113.10",2224),timeout=5); c.settimeout(5)
f=open(sys.argv[1],"w",buffering=1)
while True:
    try:
        if not c.recv(100):
            f.write("closed\n"); sys.exit(1)
        f.write("ok\n")
    except Exception as e:
        f.write("broken %r\n"%(e,)); sys.exit(1)
PY
cat >"$work/echo6.py" <<'PY'
import socket,threading
s=socket.socket(socket.AF_INET6); s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1); s.setsockopt(socket.IPPROTO_IPV6,socket.IPV6_V6ONLY,1); s.bind(("::",2222)); s.listen(16)
def h(c):
    while True:
        d=c.recv(100)
        if not d: return
        c.sendall(d)
while True:
    c,_=s.accept(); threading.Thread(target=h,args=(c,),daemon=True).start()
PY
$S python3 "$work/echo6.py" & pids+=($!)
cat >"$work/udp6.py" <<'PY'
import socket
s=socket.socket(socket.AF_INET6,socket.SOCK_DGRAM); s.setsockopt(socket.IPPROTO_IPV6,socket.IPV6_V6ONLY,1); s.bind(("::",5353))
while True:
    d,a=s.recvfrom(100); s.sendto(d,a)
PY
$S python3 "$work/udp6.py" & pids+=($!)
# 已登录的 SSH 会话：每秒收发一次
cat >"$work/long.py" <<'PY'
import socket,sys,time
c=socket.create_connection(("203.0.113.10",2222),timeout=3); c.settimeout(3)
f=open(sys.argv[1],"w",buffering=1)
while True:
    try:
        c.sendall(b"x"); assert c.recv(10)==b"x"; f.write("ok\n")
    except Exception as e:
        f.write("broken %r\n"%(e,)); sys.exit(1)
    time.sleep(1)
PY
sleep 0.5

newconn() { $C timeout 5 python3 -c 'import socket;c=socket.create_connection(("203.0.113.10",2222),timeout=4);c.settimeout(4);c.sendall(b"y");assert c.recv(5)==b"y"' 2>/dev/null && echo 通 || echo 不通; }
# 必须校验回包来源：真实的 WireGuard、DNS 客户端会丢弃来源不对的包
udp() { $C timeout 4 python3 -c 'import socket;s=socket.socket(socket.AF_INET,socket.SOCK_DGRAM);s.settimeout(3);s.sendto(b"u",("203.0.113.10",5353));d,a=s.recvfrom(5);print("通" if a==("203.0.113.10",5353) else "来源错误 %s:%d"%a)' 2>/dev/null || echo 不通; }
newconn6() { $C timeout 5 python3 -c 'import socket;c=socket.create_connection(("2001:db8:2::10",2222),timeout=4);c.settimeout(4);c.sendall(b"y");assert c.recv(5)==b"y"' 2>/dev/null && echo 通 || echo 不通; }
udp6() { $C timeout 4 python3 -c 'import socket;s=socket.socket(socket.AF_INET6,socket.SOCK_DGRAM);s.settimeout(3);s.sendto(b"u",("2001:db8:2::10",5353));d,a=s.recvfrom(5);print("通" if a[:2]==("2001:db8:2::10",5353) else "来源错误 %s:%d"%a[:2])' 2>/dev/null || echo 不通; }
# velumd 不能在对外地址上监听：否则就是开放的透明代理入口（接管用策略路由，没有重定向端口）
open_ports() { $S ss -ltnpH 2>/dev/null | grep velumd | awk '$4 !~ /^(127\.|\[::1\])/' | wc -l | tr -d ' '; }
out_http() { $S curl -s --noproxy '*' -m 5 "http://$1/" 2>/dev/null | grep -q internet-ok && echo 通 || echo 不通; }
# 接管：Velum 的策略路由规则（查路由表 7894，见 velum-engine 的 tun.rs）在不在
takeover() { $S ip -4 rule 2>/dev/null | grep -q 'lookup 7894' && echo 1 || echo 0; }
rules() { $S ip -4 rule; $S ip -6 rule; }

cat >"$work/sub.yaml" <<YAML
proxies:
  - {name: unused, type: ss, server: 192.0.2.1, port: 8388, cipher: aes-128-gcm, password: smoke}
proxy-groups:
  - {name: PROXY, type: select, proxies: [unused, DIRECT]}
rules:
  - MATCH,DIRECT
YAML
cat >"$work/config.yaml" <<YAML
listen: 127.0.0.1:7899
system_proxy: false
log_level: info
subscription:
  file: $work/sub.yaml
network_sense:
  enabled: false
YAML

start_velumd() {
  # PATH 里不能有 resolvectl：systemd-resolved 在 netns 外面，netns 里不该去查或改它（ip 按绝对路径找）
  # 以 root 运行即可开 TUN，不用服务模式：本地接口与数据都在 $work，不碰 /run/velum、/var/lib/velum
  $S env PATH=/nonexistent "$bin/velumd" --home "$work" >>"$work/velumd.out" 2>&1 &
  velumd_pid=$!
  for _ in $(seq 1 100); do
    # 本地接口先就绪，配置随后才加载完；加载期间开关请求会被拒绝
    st="$("$vlu" status 2>&1)" && ! echo "$st" | grep -q 正在启动 && return 0
    sleep 0.1
  done
  echo "velumd 没有启动"; failed=1; exit 1
}
wait_tun() {
  for _ in $(seq 1 100); do
    [ "$(takeover)" = 1 ] && return 0
    sleep 0.1
  done
  return 1
}
crash() {
  kill -9 "$velumd_pid"; wait "$velumd_pid" 2>/dev/null; velumd_pid=""
  for _ in $(seq 1 50); do
    $S ip link show velum0 >/dev/null 2>&1 || return 0
    sleep 0.1
  done
}

resolved_links() { command -v resolvectl >/dev/null && resolvectl dns 2>/dev/null | grep -c 172.19.0; }
resolved_base="$(resolved_links)"

echo "=== 基线（没有 velumd）"
check "新连接" "$(newconn)" 通
check "通配地址的 UDP" "$(udp)" 通
check "出站" "$(out_http 192.0.2.1)" 通
check "IPv6 新连接" "$(newconn6)" 通
check "IPv6 通配地址的 UDP" "$(udp6)" 通
check "IPv6 出站" "$(out_http '[2001:db8:9::1]')" 通
# 别人的规则（Tailscale、wg-quick 用的优先级）：清理时不能碰
$S ip rule add priority 5270 lookup 52
$S ip rule add priority 32764 not fwmark 0xca6c lookup 51820
base="$(rules)"

$C python3 "$work/long.py" "$work/long.log" & pids+=($!)
$C python3 "$work/pushc.py" "$work/push.log" & pids+=($!)
sleep 1.5

echo "=== 接管"
start_velumd
out="$("$vlu" tun on 2>&1)"; echo "  $out" | head -2
wait_tun; check "接管规则已建立" "$(takeover)" 1
check "TUN 网卡" "$($S ip -br link show velum0 2>/dev/null | wc -l | tr -d ' ')" 1
sleep 1
check "已有连接未断" "$(tail -1 "$work/long.log")" ok
sleep 2
check "服务端先发包的已有连接未断" "$(tail -1 "$work/push.log" | cut -c1-40)" ok
check "新连接" "$(newconn)" 通
check "通配地址的 UDP" "$(udp)" 通
check "出站" "$(out_http 192.0.2.1)" 通
check "内网可达" "$(out_http 10.9.9.9)" 通
check "IPv6 新连接" "$(newconn6)" 通
check "IPv6 通配地址的 UDP" "$(udp6)" 通
check "IPv6 出站" "$(out_http '[2001:db8:9::1]')" 通
check "没有对外的监听端口" "$(open_ports)" 0
sleep 0.5
conns="$("$vlu" conns 2>&1)"
check "出站经过 Velum" "$(echo "$conns" | grep -c '192\.0\.2\.1' | awk '{print ($1>0)?"是":"否"}')" 是
check "IPv6 出站经过 Velum" "$(echo "$conns" | grep -c '2001:db8:9::1' | awk '{print ($1>0)?"是":"否"}')" 是
check "内网不进隧道" "$(echo "$conns" | grep -c '10\.9\.9\.9' | tr -d ' ')" 0

echo "=== 崩溃三次：每次清理后再启动（systemd 单元就是这样做的）"
for i in 1 2 3; do
  crash
  [ "$i" != 1 ] || check "崩溃后有残留（否则这个测试什么也没验证）" "$(takeover)" 1
  [ "$i" != 1 ] || check "崩溃后（只剩规则、没有网卡）出站照常" "$(out_http 192.0.2.1)" 通
  $S "$bin/velumd" service cleanup-net || { echo "  ✗ cleanup-net 失败"; failed=1; }
  check "第 $i 次：接管规则已清除" "$(takeover)" 0
  check "第 $i 次：连接保护表已清除" "$($S nft list tables 2>/dev/null | grep -c velum-guard)" 0
  check "第 $i 次：ip rule 恢复原样，别人的规则还在" "$(rules)" "$base"
  check "第 $i 次：出站" "$(out_http 192.0.2.1)" 通
  check "第 $i 次：UDP" "$(udp)" 通
  check "第 $i 次：IPv6 出站" "$(out_http '[2001:db8:9::1]')" 通
  start_velumd
  wait_tun; check "第 $i 次：重启后接管恢复" "$(takeover)" 1
done
check "多次崩溃后长连接未断" "$(tail -1 "$work/long.log")" ok
check "接管恢复后：UDP" "$(udp)" 通
check "接管恢复后：出站" "$(out_http 192.0.2.1)" 通

echo "=== 正常关闭"
"$vlu" tun off >/dev/null 2>&1
for _ in $(seq 1 50); do [ "$(takeover)" = 0 ] && break; sleep 0.1; done
check "关闭后没有接管规则" "$(takeover)" 0
check "关闭后没有连接保护表" "$($S nft list tables 2>/dev/null | grep -c velum-guard)" 0
check "全程服务端先发包的连接未断" "$(tail -1 "$work/push.log" | cut -c1-40)" ok
check "关闭后 ip rule 恢复原样" "$(rules)" "$base"
check "关闭后出站" "$(out_http 192.0.2.1)" 通

check "主机的 systemd-resolved 没有被改动" "$(resolved_links)" "$resolved_base"

[ "$failed" = 0 ] && echo "netns-tun: 通过" || { echo "netns-tun: 失败"; exit 1; }
