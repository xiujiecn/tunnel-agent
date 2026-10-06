#!/bin/bash
# gofmt 检查：任一 .go 文件未格式化即失败。
#
# ★ 为什么不直接在 CI 的 run 里写 `out=$(gofmt -l .); [ -z "$out" ]`：
#   - Windows runner 默认 shell 是 PowerShell，POSIX 语法直接报错；
#   - 换成 `shell: bash` 能跑，但中文输出 + `set -euo pipefail` 在
#     Git Bash 的代码页下会出问题（Windows 默认 GBK）。
#   ⇒ 把逻辑放在仓库脚本里：跨平台行为一致，且**本地能实跑验证**。
#
# ★ 刻意只用 POSIX + bash 3 可用的语法（不用关联数组）：
#   macOS 自带 bash 3.2，没有 declare -A。
#
# ★ gofmt -d **单独用不行**：它只打印 diff、退出码恒为 0，
#   有未格式化文件时 step 照样是绿的 —— 这个坑我踩过一次。
set -uo pipefail

files=$(gofmt -l . 2>/dev/null | grep -v '^$')
if [ -n "$files" ]; then
  echo "these files are not gofmt'ed:"
  echo "$files"
  echo "--- diff ---"
  # shellcheck disable=SC2086
  gofmt -d $files
  exit 1
fi
echo "gofmt clean"
