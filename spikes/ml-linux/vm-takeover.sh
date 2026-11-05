#!/bin/bash
# ML S3～S7：在真实虚拟机上全局接管，测 SSH 入站、Docker、DNS、云元数据、防火墙、崩溃清理。
# **会接管这台机器的网络，只在可回滚的测试虚拟机上运行。**
# 用法（由 systemd-run 启动，脱离 SSH 会话）：
#   vm-takeover.sh revert                     回退：停 sing-box、删 nft 表和残留规则
#   vm-takeover.sh dryrun <sing-box> <日志>   只试跑回退：装好定时器，然后等它生效
#   vm-takeover.sh run <sing-box> <日志> <auto_redirect=true|false>
set -u
mode="$1"
TABLE=sing-box

revert() {
  systemctl stop sbtest.service 2>/dev/null
  systemctl reset-failed sbtest.service 2>/dev/null
  nft delete table inet "$TABLE" 2>/dev/null
  while ip rule | grep -qE '^(1|90[0-9][0-9]|32768):'; do
    p="$(ip rule | grep -E '^(1|90[0-9][0-9]|32768):' | head -1 | cut -d: -f1)"
    ip rule del priority "$p" || break
  done
  ip link del vltun0 2>/dev/null
  echo "revert done at $(date +%T)" >>/var/tmp/sb-revert.log
}

if [ "$mode" = revert ]; then revert; exit 0; fi

sb="$2"; log="$3"
self="$(realpath "$0")"
exec >>"$log" 2>&1

if [ "$mode" = dryrun ]; then
  ip rule add priority 9001 from all lookup 2022
  echo "dryrun: 已加一条测试规则：$(ip rule | grep -c '^9001:')"
  exit 0
fi

ar="$4"
work=/var/tmp/sbtest
mkdir -p "$work/data"
cat >"$work/sb.json" <<JSON
{"log":{"level":"info","output":"$work/sb.log"},
 "dns":{"servers":[{"type":"udp","tag":"d","server":"223.5.5.5"}]},
 "inbounds":[{"type":"tun","tag":"tun-in","interface_name":"vltun0","address":["172.19.0.1/30"],"mtu":9000,"stack":"system","auto_route":true,"auto_redirect":$ar}],
 "outbounds":[{"type":"direct","tag":"direct"}],
 "route":{"auto_detect_interface":true,"final":"direct","default_domain_resolver":"d","rules":[{"action":"sniff"},{"protocol":"dns","action":"hijack-dns"}]}}
JSON
: >"$work/sb.log"
mark() { wc -l <"$work/sb.log"; }
since() { tail -n +"$(( $1 + 1 ))" "$work/sb.log"; }
ok() { "$@" >/dev/null 2>&1 && echo 通 || echo 不通; }
# 容器能否连到对方：对方返回 403 / 404 也算连上（busybox wget 把它当作失败）
creach() { docker exec "$1" wget -T 6 -O /dev/null "http://$2/" 2>&1 | grep -qE 'server returned error|written to|saving to' && echo 通 || echo 不通; }
http() { curl -s --noproxy '*' -o /dev/null -m 8 -w '%{http_code}' "$1" 2>/dev/null; }

echo "===== $(date +%T) auto_redirect=$ar"
echo "基线：外网 $(http https://www.baidu.com)，解析 $(ok getent hosts www.baidu.com)，PVE $(ok ping -c1 -W2 192.168.3.21)"
docker rm -f sbb >/dev/null 2>&1
docker run -d --name sbb velum-busybox sh -c 'mkdir -p /w; httpd -f -p 8080 -h /w' >/dev/null; sleep 1
bip0="$(getent ahostsv4 www.baidu.com | awk 'NR==1{print $1}')"
echo "基线容器外网 IP：$(creach sbb "$bip0")"
docker rm -f sbb >/dev/null 2>&1
systemd-run --quiet --unit=sbtest --collect "$sb" run -c "$work/sb.json" -D "$work/data"
sleep 4
systemctl is-active -q sbtest && echo "sing-box 运行中" || { echo "sing-box 没有运行"; tail -n 3 "$work/sb.log"; exit 1; }
echo "nft 表：$(nft list tables 2>/dev/null | tr '\n' ' ')；ip rule $(ip rule | wc -l) 条"

echo "--- S4 DNS（systemd-resolved 存根）"
m=$(mark); echo "解析 $(ok getent hosts www.qq.com)，外网 $(http https://www.qq.com)"
since "$m" | grep -cE 'dns: exchange|hijack' | sed 's/^/DNS 经 sing-box 的日志条数：/'
since "$m" | grep -E 'www.qq.com' | head -2 | cut -c1-130
m=$(mark); echo "直接问 8.8.8.8：$(dig +time=3 +tries=1 +short @8.8.8.8 www.baidu.com 2>/dev/null | tail -1)"
since "$m" | grep -cE 'dns: exchange' | sed 's/^/  被劫持：/'

echo "--- S7 云元数据与内网：看 TCP 实际是否进 sing-box"
via() { m=$(mark); curl -s --noproxy '*' -o /dev/null -m 3 "http://$1/" ; sleep 0.3; echo "$1 进 sing-box 的记录 $(since "$m" | grep -c "$1")"; }
via 169.254.169.254
via 192.168.3.21:8006
via 10.0.0.2
via 172.31.255.1
echo "--- S5 Docker"
bip="$(getent ahostsv4 www.baidu.com | awk 'NR==1{print $1}')"
docker rm -f sbt >/dev/null 2>&1
docker run -d --name sbt -p 18080:8080 velum-busybox sh -c 'mkdir -p /w; echo c-ok >/w/index.html; httpd -f -p 8080 -h /w' >/dev/null
sleep 1
m=$(mark)
echo "容器访问外网 IP（$bip）：$(creach sbt "$bip")"
sleep 0.5; echo "  进 sing-box 的记录：$(since "$m" | grep -c "$bip")"
echo "容器 DNS：$(docker exec sbt cat /etc/resolv.conf | grep -m1 nameserver)"
echo "本机访问端口映射：$(curl -s --noproxy '*' -m 4 http://127.0.0.1:18080/ || echo 不通)"
ip -4 -br a show eth0 | awk '{print $3}' | cut -d/ -f1 >"$work/ip"
echo "本机经 eth0 地址访问映射：$(curl -s --noproxy '*' -m 4 "http://$(cat "$work/ip"):18080/" || echo 不通)"
echo "容器访问网桥网关：$(docker exec sbt wget -q -T 4 -O - http://172.17.0.1:18080/ 2>/dev/null || echo 不通)"

echo "--- 等外部从 SSH 和端口映射探测"
touch "$work/probe-now"
sleep "${PROBE_WAIT:-25}"

echo "--- S3 kill -9（此前没有动过防火墙）"
p="$(systemctl show -p MainPID --value sbtest)"
kill -9 "$p"; sleep 2
systemctl reset-failed sbtest 2>/dev/null
echo "kill -9 后：外网 443 $(http https://www.baidu.com)，外网 80 $(http http://www.baidu.com)，解析 $(ok getent hosts www.baidu.com)，inet sing-box 表 $(nft list tables 2>/dev/null | grep -c 'inet sing-box')，ip rule $(ip rule | wc -l)"
echo "  容器外网 $(creach sbt "$bip")"
nft list table inet sing-box 2>/dev/null | grep -E 'hook|redirect|tproxy|meta l4proto' | cut -c1-110 | sed 's/^/  残留：/' | head -14
nft list table inet sing-box 2>/dev/null | grep -cE 'redirect|tproxy' | sed 's/^/  残留表里的 redirect 规则：/'
echo "--- 崩溃后不清理直接重启"
systemd-run --quiet --unit=sbtest --collect "$sb" run -c "$work/sb.json" -D "$work/data"
sleep 4
systemctl is-active -q sbtest && echo "重启：运行中" || { echo "重启失败"; tail -n 2 "$work/sb.log" | cut -c1-140; }
echo "外网 $(http https://www.baidu.com)，ip rule $(ip rule | wc -l)"

echo "--- S6 ufw"
ufw allow 22/tcp >/dev/null; ufw --force enable >/dev/null
echo "ufw 开启：外网 $(http https://www.baidu.com)，解析 $(ok getent hosts www.baidu.com)，容器 $(creach sbt "$bip")"
touch "$work/probe2-now"
n=0; while [ -e "$work/probe2-now" ] && [ "$n" -lt 20 ]; do sleep 1; n=$((n + 1)); done
ufw --force disable >/dev/null
echo "ufw 关闭后：外网 $(http https://www.baidu.com)"

echo "--- 清理"
"$self" revert
sleep 1
echo "清理后：外网 $(http https://www.baidu.com)，解析 $(ok getent hosts www.baidu.com)，inet sing-box 表 $(nft list tables 2>/dev/null | grep -c 'inet sing-box')，ip rule $(ip rule | wc -l)"
docker rm -f sbt >/dev/null 2>&1
echo "===== 结束 $(date +%T)"
