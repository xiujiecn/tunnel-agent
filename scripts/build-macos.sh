#!/bin/bash
# scripts/build-macos.sh — 在 macOS 上构建 tunnel-agent 的 macOS 产物
#
# 用法（★ 必须在仓库【根目录】下执行）：
#     bash scripts/build-macos.sh [arch] [outdir]
#     bash scripts/build-macos.sh arm64 dist
#     bash scripts/build-macos.sh amd64 dist
#
# 产出（两项都出，各有用途）：
#     <outdir>/tunnel-agent-darwin-<arch>            裸二进制
#       ⇒ 老用法兼容（README 里教的就是这个），可直接跑、可被脚本调用
#     <outdir>/tunnel-agent-darwin-<arch>.app.zip    .app 包
#       ⇒ ★ 推荐给普通用户：Info.plist 里写了 LSUIElement，
#         双击后只在【菜单栏】出现 T 图标，不占 Dock、不抢焦点
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

BIN="$OUTDIR/$APP_NAME-darwin-$GOARCH"
BUNDLE="$OUTDIR/$APP_NAME.app"

# ── ① 裸二进制 ──
echo "==> 编译裸二进制"
CGO_ENABLED=1 GOOS=darwin GOARCH="$GOARCH" \
  go build -trimpath -ldflags "-s -w" -o "$BIN" .
ls -lh "$BIN"

# ── ② .app bundle ──
echo "==> 组装 .app"
rm -rf "$BUNDLE"
mkdir -p "$BUNDLE/Contents/MacOS/assets" "$BUNDLE/Contents/Resources"
cp "$BIN" "$BUNDLE/Contents/MacOS/$APP_NAME"
chmod +x "$BUNDLE/Contents/MacOS/$APP_NAME"

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

# ── ③ 打包 .app ──
echo "==> 打包 .app.zip"
ditto -c -k --keepParent "$BUNDLE" "$OUTDIR/$APP_NAME-darwin-$GOARCH.app.zip" 2>/dev/null \
  || (cd "$OUTDIR" && zip -qry "$APP_NAME-darwin-$GOARCH.app.zip" "$APP_NAME.app")

# 保留裸二进制；.app 目录本身不进产物（zip 才是），避免校验脚本把目录当文件
rm -rf "$BUNDLE"

echo
echo "==> 完成："
ls -lh "$OUTDIR/$APP_NAME-darwin-$GOARCH" "$OUTDIR/$APP_NAME-darwin-$GOARCH.app.zip"
