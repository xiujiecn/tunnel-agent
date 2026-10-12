#!/bin/bash
# 本地复现 release.yml 里「校验产物架构」那一步。
# ★ 单独成文件而不是 heredoc —— 上次用 heredoc 时中文注释混进了变量名，
#   报出 `pat?: unbound variable` 这种看不懂的错。
set -euo pipefail
fail=0
for f in dist/*; do
  base=$(basename "$f")

  # ★ 目录（如 dist/assets/）：不是产物本身，跳过
  if [ -d "$f" ]; then
    echo "  $base -> 目录，跳过"
    continue
  fi

  # ★ 非二进制产物（打包目录/压缩包/校验和）：不适用 file 架构判定
  case "$base" in
    *.zip|*.tar.gz|*.txt)
      if [ -s "$f" ]; then
        echo "  $base -> 归档/文本，跳过架构校验（存在且非空）"
      else
        echo "::error::$base 是空文件"
        fail=1
      fi
      continue
      ;;
  esac

  desc=$(file -b "$f")
  echo "  $base -> $desc"
  pat=""
  case "$base" in
    tunnel-agent-darwin-arm64)      pat="Mach-O 64-bit.*arm64" ;;
    tunnel-agent-darwin-amd64)      pat="Mach-O 64-bit.*x86_64" ;;
    tunnel-agent-linux-amd64)       pat="ELF 64-bit.*x86-64" ;;
    tunnel-agent-linux-arm64)       pat="ELF 64-bit.*aarch64" ;;
    tunnel-agent-windows-amd64.exe) pat="PE32.*x86-64" ;;
    # ★ `file` 对 Windows ARM64 的输出是 "ARM64"，不是 "aarch64"——
    #   旧写法 PE32.*aarch64 在 ubuntu 上永远匹配不上（上游一直没发现）。
    tunnel-agent-windows-arm64.exe) pat="PE32.*(ARM64|aarch64)" ;;
    *) echo "::error::产物 $base 不在预期清单里（构建步骤漏了某个平台？）"; fail=1; continue ;;
  esac
  if echo "$desc" | grep -qEi "$pat"; then
    echo "    [OK] 架构符合预期"
  else
    echo "::error::$base 架构不符（期望 ${pat}，实际 ${desc}）"
    fail=1
  fi
done
[ "$fail" -eq 0 ] || exit 1
echo "六个产物的架构都与预期一致"