//go:build darwin

package gui

// ═══════════════════════════════════════════════════════════════════════════
// macOS 状态栏托盘 —— Go 侧
//
// ★ ObjC 代码【不写在这里】：全部放在同目录的 tray_darwin.m。
//
//	原因（CI 上真实踩过的坑，日志原文）：
//	  duplicate symbol '_OBJC_CLASS_$_XJTrayTarget' in: 000000.o 000001.o
//	  ld: 2 duplicate symbols
//	如果在 .go 的 cgo preamble 里写 @interface/@implementation 并加
//	`#cgo CFLAGS: -x objective-c`，那么
//	  · cgo 生成 000000.o（内联 preamble）
//	  · cgo 还会生成 _cgo_export.c → 000001.o，【重复编译同一份 preamble】
//	⇒ ObjC 类被定义两次 ⇒ 链接失败。
//	放到 .m 文件后 cgo 只编译一次，且无需 -x objective-c（后缀已表明语言）。
// ═══════════════════════════════════════════════════════════════════════════

/*
#cgo LDFLAGS: -framework Cocoa

// ★ 必须有：C.CString / C.free 由 stdlib.h 提供。
//   把 ObjC 代码搬到 .m 之后，preamble 里只剩 extern 声明，
//   少了这个 include 就会报：
//     could not determine what C.free refers to
#include <stdlib.h>

// ── 以下实现全部在 tray_darwin.m ──
extern void xj_install_async(const char *tooltip,
                             const unsigned char *pngBytes, int pngLen, double ptSize);
extern void xj_run_app(void);      // 跑 AppKit 事件循环（阻塞，须在主线程）
extern void xj_stop_app(void);     // 让 [NSApp run] 返回
*/
import "C"

import (
	_ "embed"
	"log"
	"runtime"
	"unsafe"
)

// ★ 状态栏 Template 图标（18pt），放在 cmd/tunnel-agent/gui/assets/ 下。
//
//	go:embed 在文件缺失时会直接编译失败 ⇒ 需确保 assets/trayTemplate.png 存在。
//
//go:embed assets/trayTemplate.png
var trayIconPNG []byte

//export goTrayShow
func goTrayShow() {
	if host.Show != nil {
		go host.Show() // 不在 Cocoa 主线程里做阻塞操作
	}
}

//export goTrayStop
func goTrayStop() {
	if host.Stop != nil {
		go host.Stop()
	}
}

//export goTrayQuit
func goTrayQuit() {
	if host.Notify != nil {
		host.Notify("收到退出指令，正在关闭…")
	}
	host.Quit()     // 停 controller、关 HTTP、解锁 Run
	C.xj_stop_app() // 让 [NSApp run] 返回
}

//export goTrayReady
func goTrayReady() {
	if host.Notify != nil {
		host.Notify("已就绪：菜单栏 T 图标已出现（点它可打开菜单）")
	}
}

// runTray 接管当前线程跑 Cocoa 事件循环。
// ★ 必须在【主线程】调用 —— 见 run.go 里对 darwin 的分支：
//
//	macOS 要求所有 AppKit 操作在主线程，放 goroutine 会静默失败（就是"看不到图标"）。
func runTray(h TrayHost) {
	host = h
	runtime.LockOSThread()

	tip := C.CString("tunnel-agent")
	defer C.free(unsafe.Pointer(tip))

	if len(trayIconPNG) > 0 {
		C.xj_install_async(tip,
			(*C.uchar)(unsafe.Pointer(&trayIconPNG[0])), C.int(len(trayIconPNG)), C.double(18))
	} else {
		C.xj_install_async(tip, nil, 0, C.double(18))
	}
	C.xj_run_app() // 阻塞在 Cocoa 事件循环
}

func runHostWindow(h TrayHost, baseURL string) {
	host = h
	hostURL = baseURL
	if h.Notify != nil {
		h.Notify("控制台地址：" + baseURL)
	}
	log.Printf("macOS：菜单栏会出现一个 T 图标；控制台地址 %s", baseURL)
}
