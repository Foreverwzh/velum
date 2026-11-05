#!/bin/bash
# ML S1～S3：全局 auto_route 下入站连接的回包、权限、崩溃残留。
# 全部在 netns 里，不影响运行它的机器本身的网络：
#   cli 198.51.100.2 ── r（路由器，也是「互联网」192.0.2.1）── srv 203.0.113.10（sing-box 跑在这里）
# 用法：sudo bash netns-s1.sh <sing-box 可执行文件>
set -u
sb="$(realpath "$1")"
[ "$(id -u)" = 0 ] || { echo "需要 root"; exit 1; }
work="$(mktemp -d)"
chmod 755 "$work"
pids=()
cleanup() {
  for p in "${pids[@]:-}"; do [ -n "$p" ] && kill -9 "$p" 2>/dev/null; done
  for n in srv cli r; do ip netns del "vl$n" 2>/dev/null; done
}
trap cleanup EXIT
S="ip netns exec vlsrv"; C="ip netns exec vlcli"; R="ip netns exec vlr"

topology() {
  cleanup; pids=()
  for n in srv cli r; do ip netns add "vl$n"; ip netns exec "vl$n" ip link set lo up; done
  ip link add vs0 netns vlsrv type veth peer name vs1 netns vlr
  ip link add vc0 netns vlcli type veth peer name vc1 netns vlr
  $S ip addr add 203.0.113.10/24 dev vs0; $S ip link set vs0 up; $S ip route add default via 203.0.113.1
  $C ip addr add 198.51.100.2/24 dev vc0; $C ip link set vc0 up; $C ip route add default via 198.51.100.1
  $R ip addr add 203.0.113.1/24 dev vs1; $R ip link set vs1 up
  $R ip addr add 198.51.100.1/24 dev vc1; $R ip link set vc1 up
  $R ip addr add 192.0.2.1/32 dev lo
  $R sysctl -qw net.ipv4.ip_forward=1
  mkdir -p "$work/www"; echo internet-ok >"$work/www/index.html"
  (cd "$work/www" && exec $R python3 -m http.server 80 --bind 192.0.2.1 >/dev/null 2>&1) & pids+=($!); disown
  # srv 上「对外服务」：回显的 TCP 服务，相当于 sshd
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
  $S python3 "$work/echo.py" & pids+=($!); disown
  # srv 上绑定通配地址的 UDP 服务（相当于 WireGuard、DNS 服务）：回包没有绑定源地址
  cat >"$work/udp.py" <<'PY'
import socket
s=socket.socket(socket.AF_INET,socket.SOCK_DGRAM); s.bind(("0.0.0.0",5353))
while True:
    d,a=s.recvfrom(100); s.sendto(d,a)
PY
  $S python3 "$work/udp.py" & pids+=($!); disown
  # cli 上长连接客户端（相当于已登录的 SSH 会话）：每秒发一次，超时算断开，结果写日志
  cat >"$work/long.py" <<'PY'
import socket,sys,time
c=socket.create_connection(("203.0.113.10",2222),timeout=3); c.settimeout(3)
f=open(sys.argv[1],"w",buffering=1); n=0
while True:
    try:
        c.sendall(b"x"); assert c.recv(10)==b"x"; n+=1; f.write("ok %d\n"%n)
    except Exception as e:
        f.write("broken %r\n"%(e,)); sys.exit(1)
    time.sleep(1)
PY
  sleep 0.5
}

newconn() { $C timeout 5 python3 -c 'import socket;c=socket.create_connection(("203.0.113.10",2222),timeout=4);c.settimeout(4);c.sendall(b"y");assert c.recv(5)==b"y"' 2>/dev/null && echo 通 || echo 不通; }
out_http() { $S curl -s --noproxy '*' -m 5 http://192.0.2.1/ 2>/dev/null | grep -q internet-ok && echo 通 || echo 不通; }
udp() { $C timeout 4 python3 -c 'import socket;s=socket.socket(socket.AF_INET,socket.SOCK_DGRAM);s.settimeout(3);s.sendto(b"u",("203.0.113.10",5353));d,a=s.recvfrom(5);print("通" if a==("203.0.113.10",5353) else "来源错误 %s:%d"%a)' 2>/dev/null || echo 不通; }
pingc() { $C ping -c1 -W2 203.0.113.10 >/dev/null 2>&1 && echo 通 || echo 不通; }
longstate() { tail -1 "$work/long.log" 2>/dev/null; }

config() { # $1 = 额外的 tun 选项 JSON 片段
  cat >"$work/sb.json" <<JSON
{"log":{"level":"info","output":"$work/sb.log"},
 "inbounds":[{"type":"tun","tag":"tun-in","interface_name":"vltun0","address":["172.19.0.1/30"],"mtu":9000,"stack":"system","auto_route":true$1}],
 "outbounds":[{"type":"direct","tag":"direct"}],
 "route":{"auto_detect_interface":true,"final":"direct","rules":[{"action":"sniff"},{"protocol":"dns","action":"hijack-dns"}]}}
JSON
}

state() {
  echo "    ip rule 条数 $($S ip rule | wc -l)，table 2022 路由 $($S ip route show table 2022 2>/dev/null | wc -l)，tun $($S ip -br link | grep -c vltun0)，nft 表 $($S nft list tables 2>/dev/null | wc -l)"
}

start_sb() {
  if [ "$1" = caps ]; then
    $S setpriv --reuid=65534 --regid=65534 --clear-groups --inh-caps=+net_admin,+net_raw,+net_bind_service --ambient-caps=+net_admin,+net_raw,+net_bind_service "$sb" run -c "$work/sb.json" -D "$work/data" >/dev/null 2>"$work/sb.err" &
  else
    $S "$sb" run -c "$work/sb.json" -D "$work/data" >/dev/null 2>"$work/sb.err" &
  fi
  sbpid=$!; pids+=($sbpid); disown
}

variant() { # $1 名称，$2 tun 选项，$3 运行方式（root / caps）
  echo "=== $1（$3）"
  topology
  : >"$work/sb.log"; : >"$work/long.log"; chmod 666 "$work/sb.log"
  config "$2"
  echo "  基线：新连接 $(newconn)，UDP $(udp)，ping $(pingc)，出站 $(out_http)"
  $C python3 "$work/long.py" "$work/long.log" & pids+=($!); disown
  sleep 2
  mkdir -p "$work/data"; chmod 777 "$work/data"
  start_sb "$3"
  sleep 4
  if ! kill -0 "$sbpid" 2>/dev/null; then
    echo "  sing-box 启动失败："; { tail -n 3 "$work/sb.err"; tail -n 3 "$work/sb.log"; } | cut -c1-170 | sed 's/^/    /'
    return
  fi
  state
  : >"$work/sb.log.mark"; before=$(wc -l <"$work/sb.log")
  udp >/dev/null
  tail -n +"$((before + 1))" "$work/sb.log" | grep -E 'udp|inbound packet' | head -3 | cut -c1-150 | sed 's/^/    UDP 回包在 sing-box 日志里：/'
  echo "  接管中：长连接 [$(longstate)]，新连接 $(newconn)，UDP $(udp)，ping $(pingc)，出站 $(out_http)"
  grep -c '192.0.2.1' "$work/sb.log" | sed 's/^/    日志里经 TUN 的出站记录：/'
  sleep 8
  echo "  8 秒后：长连接 [$(longstate)]，新连接 $(newconn)"
  kill -9 "$sbpid"; sleep 2
  echo "  kill -9 后："; state
  echo "    长连接 [$(longstate)]，新连接 $(newconn)，UDP $(udp)，出站 $(out_http)"
  start_sb "$3"; sleep 4
  kill -0 "$sbpid" 2>/dev/null && r=成功 || { r=失败; tail -n 2 "$work/sb.err" | cut -c1-170; }
  echo "  崩溃后重启 $r："; state
  echo "    长连接 [$(longstate)]，新连接 $(newconn)，UDP $(udp)，出站 $(out_http)"
  kill "$sbpid" 2>/dev/null; sleep 2
  echo "  正常退出后："; state
  echo "    新连接 $(newconn)，UDP $(udp)，出站 $(out_http)"
}

"$sb" version | head -1
variant "仅 auto_route" '' root
variant "auto_route + auto_redirect" ',"auto_redirect":true' root
