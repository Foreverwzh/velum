#!/bin/sh
# 从当前 Velum 目录生成验证用配置到 run/（含订阅地址，已加入 .gitignore，不提交）。
# 与正在运行的 Velum 隔离：入口改为 7999，TUN 用 172.19.8.1/30、只接管测试网段，不改系统 DNS 与系统代理。
#   ROUTES  经隧道的网段，默认 1.0.0.1/32（全局接管时设为空：ROUTES=）
set -eu
src="${VELUM_HOME:-$HOME/Library/Application Support/Velum}"
dst="$(dirname "$0")/run"
routes="${ROUTES-1.0.0.1/32}"
mkdir -p "$dst"
inc="[]"
[ -n "$routes" ] && inc="[$(printf '%s' "$routes" | sed 's/[^,][^,]*/"&"/g')]"
sed -e 's|^listen: .*|listen: 127.0.0.1:7999|' \
    -e 's|^system_proxy: .*|system_proxy: false|' \
    -e 's|^  address: .*|  address: 172.19.8.1/30|' \
    -e 's|^  address6: .*|  address6: ""|' \
    -e "s|^  dns_hijack: .*|  dns_hijack: ${DNS_HIJACK:-false}|" \
    -e "s|^  route_include: .*|  route_include: $inc|" \
    -e 's|^log_level: .*|log_level: info|' \
    "$src/config.yaml" > "$dst/config.yaml"
# 验证与其他隧道共存：不因检测到全局 VPN 而让出 TUN
sed -i '' 's|^  yield_to_full_vpn: .*|  yield_to_full_vpn: false|' "$dst/config.yaml"
for f in overlay.yaml profiles.yaml; do [ -f "$src/$f" ] && cp "$src/$f" "$dst/$f"; done
# 只保留策略组选择，TUN 打开、系统代理关闭
python3 -c 'import json,sys; s=json.load(open(sys.argv[1])); json.dump({"selections":s.get("selections",{}),"tun":True,"system_proxy":False},open(sys.argv[2],"w"),ensure_ascii=False,indent=2)' "$src/state.json" "$dst/state.json"
grep -nE '^listen|^  (address|address6|dns_hijack|route_include):' "$dst/config.yaml"
# 订阅缓存（扩展先用缓存启动）
rm -rf "$dst/cache"
cp -R "$src/cache" "$dst/cache"
