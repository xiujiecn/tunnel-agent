//go:build windows

package gui

import (
	"fmt"
	"unsafe"

	"golang.org/x/sys/windows"
)

const (
	cfUnicodeText = 13
	gmemMoveable  = 0x0002
)

var (
	kernel32             = windows.NewLazySystemDLL("kernel32.dll")
	procOpenClipboard    = user32.NewProc("OpenClipboard")
	procEmptyClipboard   = user32.NewProc("EmptyClipboard")
	procSetClipboardData = user32.NewProc("SetClipboardData")
	procCloseClipboard   = user32.NewProc("CloseClipboard")
	procGlobalAlloc      = kernel32.NewProc("GlobalAlloc")
	procGlobalLock       = kernel32.NewProc("GlobalLock")
	procGlobalUnlock     = kernel32.NewProc("GlobalUnlock")
	procGlobalFree       = kernel32.NewProc("GlobalFree")
	procRtlMoveMemory    = kernel32.NewProc("RtlMoveMemory")
)

// copyTextDefault 经 Win32 剪贴板 API 写入 Unicode 文本。
//
// ★ 必须在打开剪贴板的同一 OS 线程上完成 Open→Close；
//
//	托盘菜单命令在 wndProc 里同步调用，天然满足。
//
// ★ 拷贝用 RtlMoveMemory（= memmove），而不是先把 GlobalLock 的返回值
// 转成 unsafe.Pointer 再 copy：
//
//	GlobalLock 返回的是 uintptr，把它反向转回 unsafe.Pointer 会被 go vet
//	判为「possible misuse of unsafe.Pointer」—— unsafe 包明文禁止一般情形
//	下这么做（GC 可能移动对象）。这里其实安全（那块内存属 Win32 全局堆，
//	不归 Go 的 GC 管），但 vet 既不认注释也不认 //go:nocheckptr，CI 上照样红。
//	改成「指针只单向传给 Win32」，vet 与 unsafe 规则同时满足。
func copyTextDefault(s string) error {
	u, err := windows.UTF16FromString(s)
	if err != nil {
		return err
	}
	nbytes := len(u) * 2

	r, _, e := procOpenClipboard.Call(0)
	if r == 0 {
		return fmt.Errorf("OpenClipboard: %w", e)
	}
	defer procCloseClipboard.Call()

	if r, _, e = procEmptyClipboard.Call(); r == 0 {
		return fmt.Errorf("EmptyClipboard: %w", e)
	}

	hMem, _, e := procGlobalAlloc.Call(gmemMoveable, uintptr(nbytes))
	if hMem == 0 {
		return fmt.Errorf("GlobalAlloc: %w", e)
	}

	ptr, _, e := procGlobalLock.Call(hMem)
	if ptr == 0 {
		procGlobalFree.Call(hMem)
		return fmt.Errorf("GlobalLock: %w", e)
	}
	// ★ &u[0] 的 uintptr 只在本次调用表达式里出现 —— 这正是 unsafe 包
	//   明确允许的用法（Go 变量在调用期间不会被 GC 移动）。
	procRtlMoveMemory.Call(ptr, uintptr(unsafe.Pointer(&u[0])), uintptr(nbytes))
	procGlobalUnlock.Call(hMem)

	r, _, e = procSetClipboardData.Call(cfUnicodeText, hMem)
	if r == 0 {
		procGlobalFree.Call(hMem)
		return fmt.Errorf("SetClipboardData: %w", e)
	}
	// 成功后剪贴板接管 hMem，不可 GlobalFree。
	return nil
}
