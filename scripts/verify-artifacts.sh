#!/bin/bash
# 本地复现 release.yml / ci.yml 里「校验产物架构」那一步。
# ★ 单独成文件而不是 heredoc —— 上次用 heredoc 时中文注释混进了变量名，
#   报出 `pat?: unbound variable` 这种看不懂的错。
#
# ★ 2026-10-12 起：产物**只有压缩包**（裸二进制一律不发）。
#   于是校验方式也必须跟着改：以前是 `file` 直接看 dist/ 里的文件，
#   现在得先解压再判架构 —— 否则「压缩包内容对不对」完全没被验证过，
#   而用户下载的恰恰是压缩包。
#
# ★ 同时反向校验：dist/ 里不允许出现裸二进制。
#   「多出来一个文件」和「少一个文件」一样是事故：
#   下载页会把它当成可选项列出来，用户下到不能用还怪我们。
set -euo pipefail

DIST="${1:-dist}"
# ★ 第二个参数：这个 job 负责哪一批产物。
#   ci.yml 把 linux/windows 与 macOS 拆成两个 job，各自只有一半产物；
#   若脚本硬性要求六个齐全，两个 job 都会红。release 汇总时再传 all。
WHAT="${2:-all}"
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

fail=0

check() {  # check <文件名> <期望架构正则>
  f="$DIST/$1"
  pat="$2"
  if [ ! -f "$f" ]; then
    echo "::error::缺产物 $1"
    fail=1
    return
  fi
  if [ ! -s "$f" ]; then
    echo "::error::$1 是空文件"
    fail=1
    return
  fi

  rm -rf "${WORK:?}"/*
  inner=""

  case "$1" in
    *.tar.gz)
      if ! tar -xzf "$f" -C "$WORK" 2>/dev/null; then
        echo "::error::$1 解压失败"
        fail=1
        return
      fi
      # macOS 归档内是 .app，二进制在 Contents/MacOS 下；Linux 归档内就是二进制
      inner="$(find "$WORK" -type f -name 'tunnel-agent*' | head -1)"
      ;;
    *.zip)
      if ! unzip -qo "$f" -d "$WORK" 2>/dev/null; then
        echo "::error::$1 解压失败"
        fail=1
        return
      fi
      inner="$(find "$WORK" -type f -name '*.exe' | head -1)"
      ;;
    *)
      echo "::error::$1 不是预期的归档类型"
      fail=1
      return
      ;;
  esac

  if [ -z "$inner" ]; then
    echo "::error::$1 里找不到可执行文件"
    fail=1
    return
  fi

  # ★ .app / 二进制里的可执行位必须还在：丢了的话 macOS 双击毫无反应，
  #   而现象和「程序有 bug」一模一样，极难定位。
  if [ ! -x "$inner" ]; then
    echo "::error::$1 解出的 $(basename "$inner") 没有可执行位（打包时未保留 mode？）"
    fail=1
  fi

  desc=$(file -b "$inner")
  if echo "$desc" | grep -qEi "$pat"; then
    echo "  $1 -> $(basename "$inner"): 架构符合预期 [OK]"
  else
    echo "::error::$1 内 $(basename "$inner") 架构不符（期望 ${pat}，实际 ${desc}）"
    fail=1
  fi
}

echo "== 校验压缩包（解压后判架构）== 批次: $WHAT"
# ★ 刻意不用 case 的 ;;& 贯穿写法和数组：macOS 自带的是 bash 3.2，
#   两者都要 bash 4+，在 mac runner 上会直接报错。
do_unix=0
do_macos=0
case "$WHAT" in
  all)   do_unix=1; do_macos=1 ;;
  unix)  do_unix=1 ;;
  macos) do_macos=1 ;;
  *) echo "::error::未知批次 $WHAT（用 all / unix / macos）"; exit 2 ;;
esac

if [ "$do_unix" = "1" ]; then
  check tunnel-agent-linux-amd64.tar.gz   "ELF 64-bit.*x86-64"
  check tunnel-agent-linux-arm64.tar.gz   "ELF 64-bit.*aarch64"
  check tunnel-agent-windows-amd64.zip    "PE32.*x86-64"
  # ★ `file` 对 Windows ARM64 的输出是 "ARM64"，不是 "aarch64"
  check tunnel-agent-windows-arm64.zip    "PE32.*(ARM64|aarch64)"
fi

if [ "$do_macos" = "1" ]; then
  check tunnel-agent-darwin-arm64.tar.gz  "Mach-O 64-bit.*arm64"
  check tunnel-agent-darwin-amd64.tar.gz  "Mach-O 64-bit.*x86_64"
fi

echo
echo "== 反查：dist/ 里不允许有裸二进制 =="
for f in "$DIST"/*; do
  [ -e "$f" ] || continue
  base=$(basename "$f")
  case "$base" in
    tunnel-agent-*.tar.gz|tunnel-agent-*.zip|SHA256SUMS.txt|.*) continue ;;
  esac
  echo "::error::dist/ 出现不该存在的产物：$base（发布只允许压缩包）"
  fail=1
done

echo
[ "$fail" -eq 0 ] || exit 1
echo "✓ 六个平台的压缩包内容与架构全部符合预期，且无多余裸文件"
