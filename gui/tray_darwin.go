//go:build darwin

package gui

// ═══════════════════════════════════════════════════════════════════════════
// macOS 状态栏托盘 —— Go 侧
//
// ★ ObjC 代码【不写在这里】：全部放在同目录的 tray_darwin.m。
//
//   原因（CI 上真实踩过的坑，日志原文）：
//     duplicate symbol '_OBJC_CLASS_$_XJTrayTarget' in: 000000.o 000001.o
//     ld: 2 duplicate symbols
//   如果在 .go 的 cgo preamble 里写 @interface/@implementation 并加
//   `#cgo CFLAGS: -x objective-c`，那么
//     · cgo 生成 000000.o（内联 preamble）
//     · cgo 还会生成 _cgo_export.c → 000001.o，【重复编译同一份 preamble】
//   ⇒ ObjC 类被定义两次 ⇒ 链接失败。
//   放到 .m 文件后 cgo 只编译一次，且无需 -x objective-c（后缀已表明语言）。
// ═══════════════════════════════════════════════════════════════════════════

/*
#cgo LDFLAGS: -framework Cocoa

// ★ 必须有：C.CString / C.free 由 stdlib.h 提供。
//   把 ObjC 代码搬到 .m 之后，preamble 里只剩 extern 声明，
//   少了这个 include 就会报：
//     could not determine what C.free refers to
#include <stdlib.h>

// ── 以下实现全部在 tray_darwin.m ──
extern void xjTrayInstall(const char *tooltip,
                          const unsigned char *pngBytes, int pngLen, double ptSize);
extern void xjTrayRun(void);      // 跑 AppKit 事件循环（阻塞，须在主线程）
extern void xjTrayStop(void);     // 让 [NSApp run] 返回
*/
import "C"

import (
	_ "embed"
	"log"
	"runtime"
	"unsafe"
)

// ★ 状态栏 Template 图标（18pt）。放在 gui/assets/ 下。
//
//	go:embed 在文件缺失时直接编译失败 ⇒ 需确保 assets/trayTemplate.png 存在。
//
//go:embed assets/trayTemplate.png
var trayIconPNG []byte

// ★ 本文件自己持有托盘回调。
//
//	注意：`host` / `hostURL` 这两个包级变量定义在 tray_windows.go（build tag: windows），
//	在 darwin 上并不存在 —— 所以这里不能用它们，否则非 Windows 平台编译失败。
var (
	darwinHost    TrayHost
	darwinHostURL string
)

//export goTrayShow
func goTrayShow() {
	if darwinHost.Show != nil {
		go darwinHost.Show() // 不在 Cocoa 主线程里做阻塞操作
	}
}

//export goTrayStop
func goTrayStop() {
	if darwinHost.Stop != nil {
		go darwinHost.Stop()
	}
}

//export goTrayQuit
func goTrayQuit() {
	log.Println("托盘：收到退出指令，正在关闭…")
	darwinHost.Quit() // 停 controller、关 HTTP、解锁 Run
	C.xjTrayStop()    // 让 [NSApp run] 返回
}

//export goTrayReady
func goTrayReady() {
	log.Println("托盘已就绪：菜单栏出现 T 图标（点它可打开菜单）")
}

// runTray 接管当前线程跑 Cocoa 事件循环。
//
// ★ 必须在【主线程】调用 —— 见 run.go 里对 darwin 的分支：
//
//	macOS 要求所有 AppKit 操作在主线程，放到 goroutine 里会静默失败
//	（表现就是"启动后看不到托盘图标"，且没有任何报错）。
func runTray(h TrayHost) {
	darwinHost = h
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	// ★ tip 用 defer free；xjTrayInstall 内部会【复制】字符串，
	//   所以返回后释放是安全的（不会悬垂）。
	tip := C.CString("tunnel-agent")
	defer C.free(unsafe.Pointer(tip))

	if len(trayIconPNG) > 0 {
		C.xjTrayInstall(tip,
			(*C.uchar)(unsafe.Pointer(&trayIconPNG[0])), C.int(len(trayIconPNG)), C.double(18))
	} else {
		C.xjTrayInstall(tip, nil, 0, C.double(18))
	}
	C.xjTrayRun() // 阻塞在 Cocoa 事件循环
}

func runHostWindow(h TrayHost, baseURL string) {
	darwinHost = h
	darwinHostURL = baseURL
	log.Printf("macOS：菜单栏会出现一个 T 图标；控制台地址 %s", baseURL)
}
