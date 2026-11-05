#!/bin/sh
# 冒烟测试：用发布产物实际跑一遍「启动 → 加载订阅 → 经本地端口上网 → 停止」，不碰系统网络设置。
# 用法：smoke.sh <velumd 与 vlu 所在目录>
# 数据放在临时目录，端口用 127.0.0.1 上的高位端口；不开 TUN、系统代理和网络感知，也不影响正在运行的 Velum。
set -eu
bin="$(cd "$1" && pwd)"
home="$(mktemp -d)"
export VELUM_HOME="$home"
unset http_proxy https_proxy all_proxy HTTP_PROXY HTTPS_PROXY ALL_PROXY no_proxy NO_PROXY
port=17899
web=17898
vlu="$bin/vlu"
srv=""

cleanup() {
  status=$?
  "$vlu" down >/dev/null 2>&1 || true
  [ -z "$srv" ] || kill "$srv" 2>/dev/null || true
  if [ "$status" -ne 0 ] && [ -f "$home/logs/velumd.log" ]; then
    echo "--- velumd.log" >&2
    tail -30 "$home/logs/velumd.log" >&2
  fi
  rm -rf "$home"
  exit "$status"
}
trap cleanup EXIT INT TERM

cat >"$home/sub.yaml" <<YAML
proxies:
  - {name: unused, type: ss, server: 192.0.2.1, port: 8388, cipher: aes-128-gcm, password: smoke}
proxy-groups:
  - {name: PROXY, type: select, proxies: [unused, DIRECT]}
rules:
  - DOMAIN,proxied.smoke.test,PROXY
  - MATCH,DIRECT
YAML
cat >"$home/config.yaml" <<YAML
listen: 127.0.0.1:$port
system_proxy: false
log_level: warn
subscription:
  file: $home/sub.yaml
network_sense:
  enabled: false
YAML

mkdir "$home/www"
echo velum-smoke-ok >"$home/www/index.html"
(cd "$home/www" && exec python3 -m http.server "$web" --bind 127.0.0.1 >/dev/null 2>&1) &
srv=$!
i=0
until curl -fs --noproxy '*' "http://127.0.0.1:$web/" >/dev/null 2>&1; do
  i=$((i + 1)); [ "$i" -lt 50 ] || { echo "smoke: 测试网页服务器没有启动" >&2; exit 1; }
  sleep 0.1
done

"$vlu" up >/dev/null
echo "smoke: velumd 已启动"
"$vlu" status | grep -q "$port" || { "$vlu" status >&2; echo "smoke: status 里没有监听端口" >&2; exit 1; }

"$vlu" why http://proxied.smoke.test/ | grep -q PROXY || { "$vlu" why http://proxied.smoke.test/ >&2; echo "smoke: why 没有命中 PROXY" >&2; exit 1; }

for proxy in "http://127.0.0.1:$port" "socks5://127.0.0.1:$port"; do
  [ "$(curl -fsS --max-time 10 --proxy "$proxy" "http://127.0.0.1:$web/")" = velum-smoke-ok ] || { echo "smoke: 经 $proxy 访问失败" >&2; exit 1; }
done
echo "smoke: HTTP 与 SOCKS5 入口可用"

[ "$(sh -c 'eval "$1"; curl -fsS --max-time 10 --noproxy "" "http://127.0.0.1:'"$web"'/"' _ "$("$vlu" env)")" = velum-smoke-ok ] || { echo "smoke: vlu env 的代理设置不可用" >&2; exit 1; }
echo "smoke: vlu env 可用"

"$vlu" down >/dev/null
i=0
while "$vlu" status >/dev/null 2>&1; do
  i=$((i + 1)); [ "$i" -lt 50 ] || { echo "smoke: velumd 没有退出" >&2; exit 1; }
  sleep 0.1
done
echo "smoke: 通过"
