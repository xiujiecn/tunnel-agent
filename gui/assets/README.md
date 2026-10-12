# tunnel-agent · 状态栏图标（macOS / Windows）

## 背景：为什么 macOS 上看不到图标

原先的实现只有三份：

| 文件 | 平台 | 状态 |
|---|---|---|
| `tray_windows.go` | Windows | ✅ 原生 `Shell_NotifyIcon` |
| `tray_linux.go` | Linux | ⚠️ 只打日志，让用浏览器控制台 |
| `tray_stub.go` | **其它（含 macOS）** | ❌ 只打一行日志 —— **没有托盘** |

所以 macOS 上「启动后看不到托盘图标」**不是 bug，是功能没做**。

本次补齐：新增 `tray_darwin.go`（Cocoa `NSStatusItem`），并把图标接进两个平台。

---

## 一、macOS：构建与验证

**必须在 macOS 上构建** —— 托盘用 Cocoa，走 cgo，`CGO_ENABLED=1` 不能跨平台交叉编译。

```bash
# 0) 装命令行工具（提供 clang）
xcode-select --install

# 1) 在仓库的 server/ 目录下执行
bash cmd/tunnel-agent/build-macos.sh          # 默认本机架构
bash cmd/tunnel-agent/build-macos.sh arm64    # 指定架构
```

产出 `server/dist/tunnel-agent.app`。

### 验证三步（缺一不可）

```bash
open server/dist/tunnel-agent.app
```
1. 看 **菜单栏右上角** 是否出现 **T** 图标
   - ★ 不是 Dock —— Dock 里【不应该】有它（`LSUIElement=true` 是有意为之）
2. 点 T ⇒ 应弹出菜单：**打开控制台 / 停止隧道 / 退出**
3. 选「打开控制台」⇒ 浏览器打开本地控制台页

### 排错

| 现象 | 检查 |
|---|---|
| 菜单栏没有 T | `ls server/dist/tunnel-agent.app/Contents/MacOS/` 确认 **`trayTemplate.png` 在** |
| 有图标但看不见 | 确认 PNG 是 **Template 图**（纯白 + 透明）；深色模式下靠系统反色，彩色图会隐形 |
| 想看日志 | 终端直接跑：`server/dist/tunnel-agent.app/Contents/MacOS/tunnel-agent -gui` |
| 首次打开被拦 | 右键 → 打开（ad-hoc 签名）；或 `xattr -dr com.apple.quarantine <app>` |
| 完全没反应 | 确认不是在 SSH 会话里跑 —— Cocoa 需要图形登录会话 |

### 关键实现约束（改代码时别踩）

- **AppKit 的所有 UI 操作必须在【主线程】**。
  `run.go` 里对 `darwin` 走的是**主线程分支**（和 Windows 一样直接 `runTray` 后 return）；
  ★ 如果把它改成 `go runTray(host)`，图标会**静默不出现** —— 这是最容易踩的坑。
- 图标必须 `setTemplate:YES`，否则深色菜单栏下不可见。
- `NSStatusItem` 的 `button` 方法不在公开头文件里，`tray_darwin.go` 里补了 category；
  删掉会编译报 `no known instance method for selector 'button'`。
- 三种 `runTray` 的 build tag 必须互斥：
  `tray_windows.go` = `windows` · `tray_linux.go` = `linux` · `tray_darwin.go` = `darwin` ·
  `tray_stub.go` = `!windows && !linux && !darwin`（★ 漏掉 `&& !darwin` 会重复定义 ⇒ 编译失败）。

---

## 二、Windows：自定义图标

原先 `addTrayIcon` 用的是 `loadIcon(32512)` = **`IDI_APPLICATION`（系统默认白板图标）**，
所以之前"加了 T 图标"没有任何效果。

现在：`appIcon()` 优先加载 **`tunnel-agent.ico`**，找不到才回退系统默认图标。

**`.ico` 查找顺序**（相对可执行文件目录）：
1. `<exe 同目录>/tunnel-agent.ico`
2. `<exe 同目录>/assets/tunnel-agent.ico`
3. 当前工作目录 `<cwd>/assets/tunnel-agent.ico`

⇒ ★ **分发时把这个 `.ico` 和 exe 放在一起**（或放到 `assets/` 子目录）。

`assets/tunnel-agent.ico` 含 16/32/48/256 四种尺寸，由脚本生成，可直接用。

---

## 三、图标资源说明

| 文件 | 用途 | 要求 |
|---|---|---|
| `assets/trayTemplate.png` | macOS 菜单栏（1x） | **18×18**，纯白 + 透明（Template） |
| `assets/trayTemplate@2x.png` | macOS 菜单栏（2x Retina） | **36×36**，同上 |
| `assets/tunnel-agent.ico` | Windows 托盘 | 含多种尺寸 |
| `assets/AppIcon.icns` | macOS 的 Dock/Finder 图标（可选） | 与**菜单栏图标是两套**，别混 |

★ **`.icns`（App 图标）和 `trayTemplate.png`（菜单栏图标）完全无关** ——
只换 `.icns` 不会让菜单栏图标变样，反之亦然。

---

## 四、回归检查（改动后必跑）

```bash
cd server
go build ./...                       # 全仓库
go test ./cmd/tunnel-agent/...       # 含 gui 包
gofmt -l cmd/tunnel-agent/           # 应无输出
```

Windows 上可以编、可以测；**macOS 侧只有到 mac 上才能真验证**（cgo）。
