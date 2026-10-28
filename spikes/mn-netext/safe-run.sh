#!/bin/sh
# 带兜底的全局接管测试：关 Velum TUN → 开验证隧道（全局接管）→ 看门狗 → 还原。
# 任一情况都会还原（停验证隧道、重新打开 Velum TUN）：
#   - 到达时限（默认 300 秒）；
#   - 验证隧道断开（扩展崩溃等）；
#   - 连续 3 次探测外网失败（每 5 秒一次）；
#   - 脚本被中断或杀掉（INT / TERM / HUP）；
#   - 手动：touch /tmp/velum-ne-restore
# 用 nohup 后台运行，与终端 / 会话脱离：nohup ./safe-run.sh 300 > safe-run.log 2>&1 &
set -u
cd "$(dirname "$0")"
LIMIT=${1:-300}
APP=/Applications/VelumNESpike.app/Contents/MacOS/VelumNESpike
VLU=/Applications/Velum.app/Contents/MacOS/vlu
PROBE=${PROBE:-https://api.anthropic.com}
STOPFILE=/tmp/velum-ne-restore

say() { echo "[$(date +%H:%M:%S)] $*"; }
probe() { curl -s -o /dev/null --max-time 5 -w "%{http_code}" "$PROBE" 2>/dev/null | grep -qv '^000$'; }

restored=0
restore() {
  [ $restored = 1 ] && return
  restored=1
  say "还原：$1"
  "$APP" stop >/dev/null 2>&1 &
  p=$!
  sleep 3
  kill $p 2>/dev/null
  scutil --nc stop "Velum NE Spike" >/dev/null 2>&1 # 宿主程序卡住时的后备
  sleep 1
  "$VLU" tun on 2>&1 | sed 's/^/  vlu: /'
  for i in 1 2 3 4 5 6; do
    if probe; then say "还原完成，外网正常"; return; fi
    sleep 2
  done
  say "还原后外网仍不通，请检查 Velum"
}
trap 'restore 收到信号; exit 1' INT TERM HUP
rm -f "$STOPFILE"

ROUTES= DNS_HIJACK=true ./prepare.sh >/dev/null
say "关闭 Velum TUN"
"$VLU" tun off 2>&1 | sed 's/^/  vlu: /'
sleep 1
say "启动验证隧道（全局接管），时限 ${LIMIT}s"
"$APP" start "$PWD/run" 2>&1 | sed 's/^/  /'

start=$(date +%s)
fails=0
while :; do
  now=$(date +%s)
  [ $((now - start)) -ge "$LIMIT" ] && { restore "到达时限"; break; }
  [ -e "$STOPFILE" ] && { restore "收到手动还原"; break; }
  # 隧道已断开（扩展崩溃、被系统或用户断开）：不等探测失败，立即还原
  scutil --nc status "Velum NE Spike" 2>/dev/null | head -1 | grep -q '^Connected$' || { restore "验证隧道已断开"; break; }
  if probe; then
    fails=0
  else
    fails=$((fails + 1))
    say "探测失败 $fails/3"
    [ $fails -ge 3 ] && { restore "外网连续不通"; break; }
  fi
  sleep 5
done
rm -f "$STOPFILE"
