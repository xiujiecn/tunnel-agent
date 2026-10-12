// tray_darwin.m — macOS 状态栏托盘（Objective-C 实现）
//
// ★ 为什么 ObjC 单独放 .m 而不是写在 .go 的 cgo preamble 里：
//
//	CI 上真实踩过的坑（日志原文）：
//	  duplicate symbol '_OBJC_CLASS_$_XJTrayTarget' in: 000000.o 000001.o
//	  ld: 2 duplicate symbols
//	在 preamble 里写 @interface/@implementation 并加 `#cgo CFLAGS: -x objective-c` 时，
//	  · cgo 生成 000000.o（内联 preamble 那份）
//	  · cgo 还会生成 _cgo_export.c → 000001.o，【同一份 preamble 被再编一次】
//	⇒ ObjC 类被定义两次 ⇒ 链接失败。
//	放 .m 之后 cgo 只编一次，也不需要 -x objective-c（后缀已表明语言）。
//
// ★ 函数不能是 static：Go 侧用 C.xj_install_async / C.xj_run_app / C.xj_stop_app
//   跨编译单元调用它们（旧版写在同一 preamble 里才可以是 static）。

#import <Cocoa/Cocoa.h>
#import <dispatch/dispatch.h>

// ── 从 Go 侧导出、给 ObjC 调用的回调（见 tray_darwin.go 的 //export）──
extern void goTrayShow(void);
extern void goTrayStop(void);
extern void goTrayQuit(void);
extern void goTrayReady(void);

// ★ NSStatusItem 的 button 方法没有出现在公开头文件里，
//   必须自己补一个 category，否则编译报 "no known instance method for selector 'button'"。
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
void xj_install_async(const char *tooltip, const unsigned char *pngBytes,
                      int pngLen, double ptSize) {
    // 先把数据复制/保留，供 main queue 之后使用（调用方的内存可能已回收）
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
                    // ★ 关键：Template ⇒ 系统按菜单栏明暗自动反色 ⇒ 深色模式也看得见
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
void xj_run_app(void) {
    @autoreleasepool {
        NSApplication *app = [NSApplication sharedApplication];
        // Accessory ⇒ 不出现在 Dock、不抢焦点，只留状态栏图标（等价 LSUIElement）
        [app setActivationPolicy:NSApplicationActivationPolicyAccessory];
        [app run];
    }
}

// ── 让 [NSApp run] 返回（可从任意线程安全调用）──
void xj_stop_app(void) {
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
