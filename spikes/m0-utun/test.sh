#!/usr/bin/env bash
# M0 utun spike 自动化验证（以普通用户运行；helper 需另开终端 sudo 启动）。
# 全程不触碰默认路由 / DNS / 系统代理；只影响 3 个测试 IP。
set -u
cd "$(dirname "$0")"
DIRECT_IP=223.6.6.6
STASH_IP=9.9.9.9
LATE_IP=149.112.112.112
LOG=/tmp/velum-spike-velumd.log
PASS=0; FAIL=0
ok()   { echo "  ✅ $*"; PASS=$((PASS+1)); }
bad()  { echo "  ❌ $*"; FAIL=$((FAIL+1)); }
iface(){ route -n get "$1" 2>/dev/null | awk '/interface:/{print $2}'; }
probe(){ curl --noproxy '*' -sS -m 8 -o /dev/null -w "%{http_code} %{time_total}s" "https://$1/" 2>&1; }

[ -S /tmp/velum-spike/helper.sock ] || { echo "helper 未运行：另开终端执行 sudo ./bin/velum-helper-spike"; exit 1; }

echo "== 0. 基线（Stash 状态）"
BASE_DEFAULT=$(route -n get default | awk '/interface:/{print $2}')
echo "  default via $BASE_DEFAULT; $DIRECT_IP via $(iface $DIRECT_IP); $STASH_IP via $(iface $STASH_IP)"

echo "== 1. 启动 velumd-spike（用户态）"
pkill -f bin/velumd-spike 2>/dev/null; sleep 0.3
: > "$LOG"
./bin/velumd-spike >>"$LOG" 2>&1 &
VPID=$!
for _ in $(seq 1 50); do grep -q "ENGINE STARTED" "$LOG" && break; sleep 0.1; done
if grep -q "ENGINE STARTED" "$LOG"; then ok "$(grep 'ENGINE STARTED' "$LOG" | sed 's/.*\] //')"; else bad "引擎未启动"; tail -30 "$LOG"; kill $VPID 2>/dev/null; exit 1; fi
TUN=$(grep -o 'name=utun[0-9]*' "$LOG" | head -1 | cut -d= -f2)
echo "  utun=$TUN, velumd uid=$(id -u) (非 root)"
ifconfig "$TUN" | sed 's/^/    /'

echo "== 2. 路由只影响测试 IP"
[ "$(iface $DIRECT_IP)" = "$TUN" ] && ok "$DIRECT_IP → $TUN" || bad "$DIRECT_IP → $(iface $DIRECT_IP)"
[ "$(iface $STASH_IP)" = "$TUN" ]  && ok "$STASH_IP → $TUN"  || bad "$STASH_IP → $(iface $STASH_IP)"
[ "$(route -n get default | awk '/interface:/{print $2}')" = "$BASE_DEFAULT" ] && ok "默认路由仍是 $BASE_DEFAULT" || bad "默认路由被改动！"

echo "== 3. 流量经 utun → sing-box"
r=$(probe $DIRECT_IP); [[ $r == 404* || $r == 200* ]] && ok "$DIRECT_IP $r" || bad "$DIRECT_IP $r"
r=$(probe $STASH_IP);  [[ $r == 404* || $r == 200* ]] && ok "$STASH_IP $r"  || bad "$STASH_IP $r"
sleep 0.5
grep -q "outbound/direct\[direct-phys\].*$DIRECT_IP" "$LOG" && ok "$DIRECT_IP 命中 direct-phys" || bad "$DIRECT_IP 未见 direct-phys 日志"
grep -q "outbound/http\[via-stash\].*$STASH_IP" "$LOG" && ok "$STASH_IP 命中 via-stash" || bad "$STASH_IP 未见 via-stash 日志"
if grep -q "OWNER tcp .*path=/usr/bin/curl" "$LOG"; then ok "进程归属（经 helper）：$(grep -m1 'OWNER tcp .*path=/usr/bin/curl' "$LOG" | sed 's/.*OWNER //')"; else bad "未查到 curl 进程归属"; grep -m3 OWNER "$LOG" | sed 's/^/    /'; fi

echo "== 4. 运行期动态路由（不重启引擎）"
before=$(probe $LATE_IP); echo "  before: $LATE_IP via $(iface $LATE_IP) $before"
kill -USR1 $VPID; sleep 0.5
[ "$(iface $LATE_IP)" = "$TUN" ] && ok "SIGUSR1 后 $LATE_IP → $TUN" || bad "$LATE_IP → $(iface $LATE_IP)"
r=$(probe $LATE_IP); [[ $r == 404* || $r == 200* ]] && ok "$LATE_IP $r" || bad "$LATE_IP $r"
kill -USR2 $VPID; sleep 0.5
[ "$(iface $LATE_IP)" != "$TUN" ] && ok "SIGUSR2 后 $LATE_IP 回到 $(iface $LATE_IP)" || bad "路由未删除"
[ "$(grep -c 'ENGINE STARTED' "$LOG")" = 1 ] && ok "引擎全程只启动 1 次" || bad "引擎被重启"

echo "== 5. kill -9 兜底清理"
t0=$(python3 -c 'import time;print(time.time())')
kill -9 $VPID
for _ in $(seq 1 100); do [ "$(iface $DIRECT_IP)" != "$TUN" ] && break; sleep 0.01; done
t1=$(python3 -c 'import time;print(time.time())')
ms=$(python3 -c "print(int(($t1-$t0)*1000))")
[ "$(iface $DIRECT_IP)" != "$TUN" ] && ok "路由已还原（${ms}ms）：$DIRECT_IP → $(iface $DIRECT_IP)" || bad "kill -9 后路由残留"
ifconfig "$TUN" >/dev/null 2>&1 && bad "$TUN 仍存在" || ok "$TUN 已销毁"
r=$(probe $DIRECT_IP); [[ $r == 404* || $r == 200* ]] && ok "清理后 $DIRECT_IP 恢复经 Stash 访问 $r" || bad "$DIRECT_IP $r"

echo
echo "通过 $PASS 项，失败 $FAIL 项。完整日志：$LOG"
