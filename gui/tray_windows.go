//go:build windows

package gui

import (
	"log"
	"os"
	"path/filepath"
	"runtime"
	"syscall"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	wmClose   = 0x0010
	wmDestroy = 0x0002
	wmCommand = 0x0111
	wmTray    = 0x0400 + 1

	nimAdd    = 0x00000000
	nimDelete = 0x00000002
	nifMsg    = 0x00000001
	nifIcon   = 0x00000002
	imageIcon = 1 // IMAGE_ICON
	nifTip    = 0x00000004

	mfSeparator = 0x00000800

	swHide = 0
)

var (
	user32                  = windows.NewLazySystemDLL("user32.dll")
	shell32                 = windows.NewLazySystemDLL("shell32.dll")
	procRegisterClassW      = user32.NewProc("RegisterClassW")
	procCreateWindowExW     = user32.NewProc("CreateWindowExW")
	procDefWindowProcW      = user32.NewProc("DefWindowProcW")
	procGetMessageW         = user32.NewProc("GetMessageW")
	procTranslateMessage    = user32.NewProc("TranslateMessage")
	procDispatchMessageW    = user32.NewProc("DispatchMessageW")
	procPostQuitMessage     = user32.NewProc("PostQuitMessage")
	procShowWindow          = user32.NewProc("ShowWindow")
	procDestroyWindow       = user32.NewProc("DestroyWindow")
	procLoadIconW           = user32.NewProc("LoadIconW")
	procLoadImageW          = user32.NewProc("LoadImageW") // ★ 用于从 .ico 文件加载自定义托盘图标
	procLoadCursorW         = user32.NewProc("LoadCursorW")
	procTrackPopupMenu      = user32.NewProc("TrackPopupMenu")
	procCreatePopupMenu     = user32.NewProc("CreatePopupMenu")
	procAppendMenuW         = user32.NewProc("AppendMenuW")
	procSetForegroundWindow = user32.NewProc("SetForegroundWindow")
	procGetCursorPos        = user32.NewProc("GetCursorPos")
	procShellNotifyIconW    = shell32.NewProc("Shell_NotifyIconW")
)

type wndclass struct {
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     windows.Handle
	hIcon         windows.Handle
	hCursor       windows.Handle
	hbrBackground windows.Handle
	lpszMenuName  *uint16
	lpszClassName *uint16
}

type notifyIconData struct {
	cbSize           uint32
	hWnd             windows.Handle
	uID              uint32
	uFlags           uint32
	uCallbackMessage uint32
	hIcon            windows.Handle
	szTip            [128]uint16
}

type point struct {
	x, y int32
}

type msg struct {
	hwnd    windows.Handle
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      point
}

var (
	hostHWND windows.Handle
	inst     windows.Handle
)

func runTray(h TrayHost) {
	host = h
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	inst = windows.Handle(windows.CurrentProcess())
	className, _ := windows.UTF16PtrFromString("TunnelAgentConsole")
	wc := wndclass{
		lpfnWndProc:   syscall.NewCallback(wndProc),
		hInstance:     inst,
		hIcon:         appIcon(), // ★ 优先 tunnel-agent.ico，回退 IDI_APPLICATION
		hCursor:       loadCursor(32512),
		lpszClassName: className,
	}
	if r, _, _ := procRegisterClassW.Call(uintptr(unsafe.Pointer(&wc))); r == 0 {
		log.Println("Windows 窗口类注册失败，托盘降级为仅浏览器控制台")
		waitFallback()
		return
	}

	title, _ := windows.UTF16PtrFromString("tunnel-agent 控制台")
	hwnd, _, _ := procCreateWindowExW.Call(
		0,
		uintptr(unsafe.Pointer(className)),
		uintptr(unsafe.Pointer(title)),
		0x00CF0000, // WS_OVERLAPPEDWINDOW
		100, 100, 520, 160,
		0, 0, uintptr(inst), 0,
	)
	hostHWND = windows.Handle(hwnd)
	addTrayIcon(hostHWND)

	showHostWindow()
	messageLoop()
}

func runHostWindow(h TrayHost, baseURL string) {
	host = h
	hostURL = baseURL
}

func showHostWindow() {
	if host.Show != nil {
		host.Show()
	}
	if hostHWND != 0 {
		procShowWindow.Call(uintptr(hostHWND), 5) // SW_SHOW
	}
}

func waitFallback() {
	select {}
}

func messageLoop() {
	var m msg
	for {
		ret, _, _ := procGetMessageW.Call(uintptr(unsafe.Pointer(&m)), 0, 0, 0)
		if int32(ret) <= 0 {
			return
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&m)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&m)))
	}
}

func wndProc(hwnd windows.Handle, msg uint32, wParam, lParam uintptr) uintptr {
	switch msg {
	case wmTray:
		if lParam == 0x0203 { // WM_LBUTTONDBLCLK
			showHostWindow()
			return 0
		}
		if lParam == 0x0205 { // WM_RBUTTONUP
			showTrayMenu(hwnd)
			return 0
		}
	case wmCommand:
		// ★ 用 if 链而非 case 常量：id 冲突时仍能编译，由 TestTrayMenuIDsUnique 抓回归。
		cmd := int(wParam)
		switch {
		case cmd == idmShow:
			showHostWindow()
		case cmd == idmOpen:
			openConfigPage()
		case cmd == idmCopy:
			// ★ 剪贴板 Open/Close 与本 wndProc 同 OS 线程（消息循环已 LockOSThread）。
			copyConfigURL()
		case cmd == idmStop:
			if host.Stop != nil {
				host.Stop()
			}
		case cmd == idmQuit:
			delTrayIcon(hwnd)
			if host.Quit != nil {
				host.Quit()
			}
			procDestroyWindow.Call(uintptr(hwnd))
			procPostQuitMessage.Call(0)
		}
		return 0
	case wmClose:
		// ★ 关窗 = 收进托盘，不退出（负责人硬要求）。
		procShowWindow.Call(uintptr(hwnd), swHide)
		return 0
	case wmDestroy:
		delTrayIcon(hwnd)
		procPostQuitMessage.Call(0)
		return 0
	}
	r, _, _ := procDefWindowProcW.Call(uintptr(hwnd), uintptr(msg), wParam, lParam)
	return r
}

func showTrayMenu(hwnd windows.Handle) {
	menu, _, _ := procCreatePopupMenu.Call()
	appendMenu(menu, idmShow, "显示窗口")
	appendMenu(menu, idmOpen, "直接打开配置页")
	appendMenu(menu, idmCopy, "复制配置页地址")
	appendSeparator(menu)
	appendMenu(menu, idmStop, "停止全部")
	appendMenu(menu, idmQuit, "退出")
	procSetForegroundWindow.Call(uintptr(hwnd))
	var p point
	procGetCursorPos.Call(uintptr(unsafe.Pointer(&p)))
	procTrackPopupMenu.Call(menu, 0x0002|0x0000, uintptr(p.x), uintptr(p.y), uintptr(hwnd), 0)
}

func appendMenu(menu uintptr, id int, text string) {
	s, _ := windows.UTF16PtrFromString(text)
	procAppendMenuW.Call(menu, 0, uintptr(id), uintptr(unsafe.Pointer(s)))
}

func appendSeparator(menu uintptr) {
	procAppendMenuW.Call(menu, mfSeparator, 0, 0)
}

func addTrayIcon(hwnd windows.Handle) {
	var nid notifyIconData
	nid.cbSize = 504 // NOTIFYICONDATAW（64-bit）
	nid.hWnd = hwnd
	nid.uID = 1
	nid.uFlags = nifMsg | nifIcon | nifTip
	nid.uCallbackMessage = wmTray
	nid.hIcon = loadIcon(32512)
	copy(nid.szTip[:], utf16("tunnel-agent"))
	procShellNotifyIconW.Call(nimAdd, uintptr(unsafe.Pointer(&nid)))
}

func delTrayIcon(hwnd windows.Handle) {
	var nid notifyIconData
	nid.cbSize = 504 // NOTIFYICONDATAW（64-bit）
	nid.hWnd = hwnd
	nid.uID = 1
	procShellNotifyIconW.Call(nimDelete, uintptr(unsafe.Pointer(&nid)))
}

// loadAppIcon 尝试加载与可执行文件同目录（或 assets/）下的 tunnel-agent.ico。
// ★ 找不到时返回 0，调用方回退到系统默认图标（保持零依赖、不 panic）。
func appIcon() windows.Handle {
	if h := loadAppIcon(); h != 0 {
		return h
	}
	return loadIcon(32512) // IDI_APPLICATION
}

func loadAppIcon() windows.Handle {
	cands := []string{}
	if exe, err := os.Executable(); err == nil {
		dir := filepath.Dir(exe)
		cands = append(cands,
			filepath.Join(dir, "tunnel-agent.ico"),
			filepath.Join(dir, "assets", "tunnel-agent.ico"),
		)
	}
	cands = append(cands, filepath.Join("assets", "tunnel-agent.ico"))
	for _, c := range cands {
		if _, err := os.Stat(c); err != nil {
			continue
		}
		p, err := windows.UTF16PtrFromString(c)
		if err != nil {
			continue
		}
		// LR_LOADFROMFILE(0x0010) | LR_DEFAULTSIZE(0x0040)
		r, _, _ := procLoadImageW.Call(0, uintptr(unsafe.Pointer(p)), imageIcon, 0, 0, 0x0010|0x0040)
		if r != 0 {
			log.Printf("托盘图标：%s", c)
			return windows.Handle(r)
		}
	}
	log.Println("托盘图标：未找到 tunnel-agent.ico，回退系统默认图标")
	return 0
}

func loadIcon(id uintptr) windows.Handle {
	r, _, _ := procLoadIconW.Call(0, id)
	return windows.Handle(r)
}

func loadCursor(id uintptr) windows.Handle {
	r, _, _ := procLoadCursorW.Call(0, id)
	return windows.Handle(r)
}

func utf16(s string) []uint16 {
	u, _ := windows.UTF16FromString(s)
	return u
}
