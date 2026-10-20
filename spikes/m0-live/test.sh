#!/usr/bin/env bash
# M0 动态实录验证：本地 mixed 入站 127.0.0.1:17891，不需要 root，不碰 TUN / 系统代理。
set -u
cd "$(dirname "$0")"
BIN=${BIN:-/tmp/velum-live-bin}
REC=/tmp/velum-live/records.jsonl
P=http://127.0.0.1:17891
mkdir -p /tmp/velum-live; pkill -f "$BIN" 2>/dev/null; sleep 0.3
"$BIN" > /tmp/velum-live/stdout.log 2>&1 &
PID=$!
for _ in $(seq 1 50); do grep -q "LIVE STARTED" /tmp/velum-live/stdout.log 2>/dev/null && break; sleep 0.1; done
grep "LIVE STARTED" /tmp/velum-live/stdout.log || { cat /tmp/velum-live/stdout.log; exit 1; }

c() { curl -sS -m 8 -o /dev/null -w "%{http_code}" "$@" 2>&1 | tail -c 60; echo; }
echo "a. IP 直连           $(c -x $P https://223.6.6.6/)"
echo "b. 域名直连           $(c -x $P https://www.baidu.com/)"
echo "c. 策略组链           $(c -x $P https://9.9.9.9/)"
echo "d. 解析后 final       $(c -x $P https://example.org/)"
echo "e. reject 动作        $(c -x $P https://ads.example.com/)"
echo "f. block 出站         $(c -x $P https://blocked.example.com/)"
echo "g. 拨号超时           $(c -x $P https://10.255.255.1/)"
echo "h. SOCKS5 IP + SNI 嗅探 $(c --socks5 127.0.0.1:17891 https://www.baidu.com/)"
sleep 3
kill $PID; wait $PID 2>/dev/null
echo; echo "== 记录 =="
ruby -rjson -e 'File.readlines(ARGV[0]).each{|l| r=JSON.parse(l)
 if r["untracked"] then puts "  [未经 tracker] id=#{r["id"]} #{r["level"]}: #{r["log"][0,110]}"; next end
 puts "  id=#{r["id"]} #{r["domain"].to_s.empty? ? r["destination"] : r["domain"]+" ("+r["destination"]+")"} sniff=#{r["sniffed"]} resolved=#{r["resolved"]}"
 puts "     rule[#{r["rule_index"]}] #{r["rule"]}  ←  #{r["provenance"]}"
 puts "     chain=#{r["chain"].join(" → ")}  up=#{r["upload"]}B down=#{r["download"]}B first_byte=#{r["first_byte_ms"]}ms dur=#{r["duration_ms"]}ms#{r["errors"] ? "  errors="+r["errors"].map{|e|e[0,90]}.to_s : ""}"
}' $REC
