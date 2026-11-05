#!/bin/sh
# 安装 Velum 系统服务（Linux，需要 root 和 systemd）。重复执行就是升级，配置不变。
#   sudo sh install.sh                     与 velumd、vlu 同目录时：安装这份
#   sudo sh install.sh <压缩包>            安装本地压缩包（无法访问 GitHub 时用）；同目录有 SHA256SUMS 时校验
#   sudo sh install.sh --version 0.1.0     下载指定版本并校验；不带参数时下载最新版本（含预发布版本）
set -eu
repo=Foreverwzh/velum
here="$(cd "$(dirname "$0")" 2>/dev/null && pwd || pwd)"
die() { echo "install.sh: $*" >&2; exit 1; }
[ "$(id -u)" = 0 ] || die "需要 root（用 sudo 运行）"

tmp="$(mktemp -d)"
trap 'rm -rf "$tmp"' EXIT INT TERM

# 校验压缩包：$1 压缩包，$2 校验和文件
verify() {
  want="$(awk -v f="$(basename "$1")" '$2 == f {print $1}' "$2")"
  [ -n "$want" ] || die "$2 里没有 $(basename "$1")"
  [ "$(sha256sum "$1" | cut -d' ' -f1)" = "$want" ] || die "$(basename "$1") 校验失败"
  echo "校验通过：$(basename "$1")"
}

unpack() {
  tar -xzf "$1" -C "$tmp"
  bin="$(dirname "$(find "$tmp" -type f -name velumd | head -1)")"
  [ -x "$bin/velumd" ] && [ -x "$bin/vlu" ] || die "压缩包里没有 velumd、vlu"
}

version=""
file=""
while [ $# -gt 0 ]; do
  case "$1" in
    --version) [ $# -ge 2 ] || die "--version 需要版本号"; version="${2#v}"; shift 2 ;;
    -*) die "未知参数 $1" ;;
    *) file="$1"; shift ;;
  esac
done

if [ -n "$file" ]; then
  [ -f "$file" ] || die "没有这个文件：$file"
  sums="$(dirname "$file")/SHA256SUMS"
  [ ! -f "$sums" ] && echo "提示：没有 SHA256SUMS，跳过校验" || verify "$file" "$sums"
  unpack "$file"
elif [ -z "$version" ] && [ -x "$here/velumd" ] && [ -x "$here/vlu" ]; then
  bin="$here"
else
  case "$(uname -m)" in
    x86_64) arch=x64 ;;
    aarch64|arm64) arch=arm64 ;;
    *) die "不支持的架构 $(uname -m)" ;;
  esac
  if [ -z "$version" ]; then
    version="$(curl -fsSL "https://api.github.com/repos/$repo/releases?per_page=1" | sed -n 's/.*"tag_name": *"v\{0,1\}\([^"]*\)".*/\1/p' | head -1)"
    [ -n "$version" ] || die "查不到最新版本；可以用 --version 指定，或下载压缩包后本地安装"
  fi
  name="Velum-$version-linux-$arch.tar.gz"
  base="https://github.com/$repo/releases/download/v$version"
  echo "下载 $name"
  curl -fsSL --retry 3 --retry-delay 3 "$base/$name" -o "$tmp/$name" || die "下载失败；可以下载压缩包后本地安装"
  curl -fsSL --retry 3 --retry-delay 3 "$base/SHA256SUMS" -o "$tmp/SHA256SUMS" || die "下载校验和失败"
  verify "$tmp/$name" "$tmp/SHA256SUMS"
  unpack "$tmp/$name"
fi

"$bin/velumd" service install
