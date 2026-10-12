#!/bin/bash
# scripts/build-macos.sh — 在 macOS 上构建 tunnel-agent 的 macOS 产物
#
# 用法（★ 必须在仓库【根目录】下执行）：
#     bash scripts/build-macos.sh [arch] [outdir]
#     bash scripts/build-macos.sh arm64 dist
#     bash scripts/build-macos.sh amd64 dist
#
# 产出（★ 只有压缩包 —— 裸二进制不进产物）：
#     <outdir>/tunnel-agent-darwin-<arch>.tar.gz
#       ⇒ 内含 tunnel-agent.app。Info.plist 写了 LSUIElement，
#         双击后只在【菜单栏】出现 T 图标，不占 Dock、不抢焦点；
#         应用图标由 Contents/Resources/AppIcon.icns 提供。
#       ⇒ ★ 用 tar.gz 而不是 zip：tar 会保留可执行位（755），
#         zip 在部分解压工具下会丢掉，导致双击无反应。
#
# ★ 为什么产物里不留裸二进制：
#   裸文件（无扩展名、无签名、无 bundle）落到用户手里既不能双击、
#   又会被 Gatekeeper 拦，下载页也从不下发它 —— 留着只会让
#   「该下哪个」变成一道选择题。需要裸二进制的场景（本地调试）
#   在本脚本内部用临时目录完成，不进 outdir。
#
# ★ 为什么必须在 macOS 上构建（而不是像其它平台那样从 ubuntu 交叉编译）：
#   状态栏托盘用 Cocoa（NSStatusItem），走 cgo ⇒ CGO_ENABLED=1，
#   而 cgo **不能跨平台交叉编译**。在 Linux/Windows 上编 darwin 会直接报
#   `undefined: runTray / runHostWindow`（那些文件带 //go:build darwin 且含 cgo，
#   非 darwin 主机上根本不参与构建）。
set -euo pipefail

ARCH="${1:-$(uname -m)}"
OUTDIR="${2:-dist}"

case "$ARCH" in
  arm64|aarch64) GOARCH=arm64 ;;
  x86_64|amd64)  GOARCH=amd64 ;;
  *) echo "未知架构：$ARCH（用 arm64 或 amd64）" >&2; exit 2 ;;
esac

APP_NAME="tunnel-agent"
BUNDLE_ID="cn.xiujie.tunnel-agent"

# 版本号：优先用 git tag（CI 上就是触发的那支 tag），否则取短 SHA
if VERSION="$(git describe --tags --always --dirty 2>/dev/null)"; then
  :
else
  VERSION="0.0.0-$(git rev-parse --short HEAD 2>/dev/null || echo unknown)"
fi

echo "==> 目标 darwin/$GOARCH    版本 $VERSION    输出 $OUTDIR"
mkdir -p "$OUTDIR"
OUTDIR="$(cd "$OUTDIR" && pwd)"

# 裸二进制与 .app 都放临时目录：裸二进制只作为 .app 的输入，不进产物
WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
BIN="$WORK/$APP_NAME-darwin-$GOARCH"
BUNDLE="$WORK/$APP_NAME.app"
TARBALL="$OUTDIR/$APP_NAME-darwin-$GOARCH.tar.gz"

# ── ① 裸二进制 ──
echo "==> 编译裸二进制（CGO_ENABLED=1，链接 Cocoa）"
CGO_ENABLED=1 GOOS=darwin GOARCH="$GOARCH" \
  go build -trimpath -ldflags "-s -w" -o "$BIN" .
ls -lh "$BIN"

# ── ② .app bundle ──
echo "==> 组装 .app"
rm -rf "$BUNDLE"
mkdir -p "$BUNDLE/Contents/MacOS/assets" "$BUNDLE/Contents/Resources"
cp "$BIN" "$BUNDLE/Contents/MacOS/$APP_NAME"
chmod +x "$BUNDLE/Contents/MacOS/$APP_NAME"

# ★ 应用图标（Finder / 快速查看 / 关于本机 里看到的那个）：
#   与托盘图标是两套东西 —— 托盘是菜单栏里的 Template 图（单色自适应），
#   这个是 Finder 用的彩色 .icns。LSUIElement 下不显示 Dock 图标，
#   但 Finder 里仍按它显示。
if [ -f gui/assets/AppIcon.icns ]; then
  cp gui/assets/AppIcon.icns "$BUNDLE/Contents/Resources/AppIcon.icns"
  echo "    已放入 Contents/Resources/AppIcon.icns"
else
  echo "    ⚠️ 未找到 gui/assets/AppIcon.icns —— 应用图标将缺失" >&2
fi

# ★ 状态栏 Template 图标必须随包分发：
#   tray_darwin.go 用 go:embed 把 trayTemplate.png 编进了二进制本身，
#   但 Windows 的 .ico 是运行时按文件查找的 ⇒ 一并带上，方便同包复用。
cp gui/assets/trayTemplate.png    "$BUNDLE/Contents/MacOS/"        2>/dev/null || true
cp gui/assets/trayTemplate@2x.png "$BUNDLE/Contents/MacOS/"        2>/dev/null || true
cp gui/assets/*.png gui/assets/*.ico "$BUNDLE/Contents/MacOS/assets/" 2>/dev/null || true

cat > "$BUNDLE/Contents/Info.plist" <<PLIST
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
  <key>CFBundleName</key><string>${APP_NAME}</string>
  <key>CFBundleDisplayName</key><string>内网穿透</string>
  <key>CFBundleIdentifier</key><string>${BUNDLE_ID}</string>
  <key>CFBundleVersion</key><string>${VERSION}</string>
  <key>CFBundleShortVersionString</key><string>${VERSION}</string>
  <key>CFBundleExecutable</key><string>${APP_NAME}</string>
  <key>CFBundlePackageType</key><string>APPL</string>
  <key>CFBundleIconFile</key><string>AppIcon</string>
  <key>LSMinimumSystemVersion</key><string>11.0</string>
  <!-- ★ 关键：Accessory ⇒ 不显示 Dock 图标、不抢焦点，只在菜单栏出现 T -->
  <key>LSUIElement</key><true/>
  <key>NSHighResolutionCapable</key><true/>
</dict>
</plist>
PLIST

# ad-hoc 签名：自用足够；正式分发需要开发者证书
if command -v codesign >/dev/null 2>&1; then
  codesign --force --deep --sign - "$BUNDLE" 2>/dev/null \
    && echo "==> 已 ad-hoc 签名" \
    || echo "==> ⚠️ 签名失败（可忽略；首次打开可能需右键→打开）"
fi

# ── ③ 打包 .app → tar.gz ──
# ★ 用 tar 而不是 zip/ditto：tar 保留可执行位（755），
#   zip 在某些解压工具下会丢，那会导致双击 .app 毫无反应。
echo "==> 打包 .app.tar.gz"
rm -f "$TARBALL"
# -C 到临时目录：让归档内顶层就是 tunnel-agent.app，不带任何路径前缀
tar -czf "$TARBALL" -C "$WORK" "$APP_NAME.app"

echo
echo "==> 完成："
ls -lh "$TARBALL"
echo "==> 归档内容："
tar -tzf "$TARBALL" | sed 's/^/    /'
