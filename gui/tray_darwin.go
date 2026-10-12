//go:build darwin

package gui

/*
#cgo CFLAGS: -x objective-c
#cgo LDFLAGS: -framework Cocoa

#import <Cocoa/Cocoa.h>
#import <dispatch/dispatch.h>

// ── 从 Go 侧导出、给 ObjC 调用的回调 ──
extern void goTrayShow(void);
extern void goTrayStop(void);
extern void goTrayQuit(void);
extern void goTrayReady(void);

// ★ NSStatusItem 的 button 方法没有出现在公开头文件里，
//   必须自己补一个 category，否则编译报
//   "no known instance method for selector 'button'"。
@interface NSStatusItem (XJButton)
- (NSStatusBarButton *)button;
@end

static NSStatusItem *gStatusItem = nil;
static NSImage      *gIcon       = nil;
static id            gTarget     = nil;

@interface XJTrayTarget : NSObject
- (void)onShow:(id)sender;
- (void)onStop:(id)sender;
- (void)onQuit:(id)sender;
@end

@implementation XJTrayTarget
- (void)onShow:(id)sender { goTrayShow(); }
- (void)onStop:(id)sender { goTrayStop(); }
- (void)onQuit:(id)sender { goTrayQuit(); }
@end

// ── 在主线程创建状态栏项（AppKit 要求；非主线程会静默失败）──
static void xj_install_async(const char *tooltip, const unsigned char *pngBytes,
                             int pngLen, double ptSize) {
    // 先把数据复制/保留，供 main queue 之后使用（调用方内存可能已回收）
    NSData *pngCopy = nil;
    if (pngBytes != NULL && pngLen > 0) {
        pngCopy = [[NSData alloc] initWithBytes:pngBytes length:(NSUInteger)pngLen];
    }
    NSString *tip = nil;
    if (tooltip != NULL) {
        tip = [[NSString alloc] initWithUTF8String:tooltip];
    }

    dispatch_async(dispatch_get_main_queue(), ^{
        @autoreleasepool {
            if (gStatusItem != nil) {
                [pngCopy release];
                [tip release];
                return;
            }

            if (pngCopy != nil) {
                NSImage *img = [[NSImage alloc] initWithData:pngCopy];
                if (img != nil) {
                    // ★ 关键：Template ⇒ 系统按菜单栏明暗自动反色 ⇒ 深色模式也可见
                    [img setTemplate:YES];
                    [img setSize:NSMakeSize(ptSize, ptSize)];
                    gIcon = img;   // 持有到进程结束
                }
            }
            [pngCopy release];

            gStatusItem = [[NSStatusBar systemStatusBar]
                           statusItemWithLength:NSSquareStatusItemLength];
            [gStatusItem retain];

            NSStatusBarButton *btn = [gStatusItem button];
            if (btn != nil) {
                if (gIcon != nil) {
                    [btn setImage:gIcon];
                    [btn setImagePosition:NSImageOnly];
                } else {
                    // 退化路径：至少给一个可见的文字入口
                    [btn setTitle:@"T"];
                }
            }

            gTarget = [[XJTrayTarget alloc] init];

            NSMenu *menu = [[NSMenu alloc] init];
            [menu setAutoenablesItems:NO];

            NSMenuItem *miShow = [[NSMenuItem alloc] initWithTitle:@"打开控制台"
                                                           action:@selector(onShow:)
                                                    keyEquivalent:@""];
            [miShow setTarget:gTarget];
            [menu addItem:miShow];
            [miShow release];

            NSMenuItem *miStop = [[NSMenuItem alloc] initWithTitle:@"停止隧道"
                                                           action:@selector(onStop:)
                                                    keyEquivalent:@""];
            [miStop setTarget:gTarget];
            [menu addItem:miStop];
            [miStop release];

            [menu addItem:[NSMenuItem separatorItem]];

            NSMenuItem *miQuit = [[NSMenuItem alloc] initWithTitle:@"退出"
                                                           action:@selector(onQuit:)
                                                    keyEquivalent:@"q"];
            [miQuit setTarget:gTarget];
            [menu addItem:miQuit];
            [miQuit release];

            [gStatusItem setMenu:menu];
            [menu release];

            if (tip != nil) {
                [gStatusItem setToolTip:tip];
            }
            [tip release];

            goTrayReady();
        }
    });
}

// ── 跑 AppKit 事件循环（阻塞；必须在主线程调用）──
static void xj_run_app(void) {
    @autoreleasepool {
        NSApplication *app = [NSApplication sharedApplication];
        // Accessory ⇒ 不出现在 Dock、不抢焦点，只留状态栏图标（等价 LSUIElement）
        [app setActivationPolicy:NSApplicationActivationPolicyAccessory];
        [app run];
    }
}

// ── 让 [NSApp run] 返回（可从任意线程安全调用）──
static void xj_stop_app(void) {
    dispatch_async(dispatch_get_main_queue(), ^{
        [NSApp stop:nil];
        NSEvent *ev = [NSEvent otherEventWithType:NSEventTypeApplicationDefined
                                         location:NSMakePoint(0, 0)
                                    modifierFlags:0
                                        timestamp:0
                                     windowNumber:0
                                          context:nil
                                          subtype:0
                                            data1:0
                                            data2:0];
        [NSApp postEvent:ev atStart:YES];
    });
}
*/
import "C"

import (
	_ "embed"
	"log"
	"runtime"
	"unsafe"
)

// ★ 状态栏 Template 图标（18pt），放在 gui/assets/ 下。
//
//	go:embed 在文件缺失时直接编译失败 ⇒ 需确保 gui/assets/trayTemplate.png 存在。
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
	C.xj_stop_app()   // 让 [NSApp run] 返回
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
	darwinHost = h
	darwinHostURL = baseURL
	log.Printf("macOS：菜单栏会出现一个 T 图标；控制台地址 %s", baseURL)
}
