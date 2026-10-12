// tray_darwin.m —— macOS 状态栏托盘的 ObjC 实现
//
// ★ 为什么单独放 .m 文件而不是写在 .go 的 cgo preamble 里：
//   cgo 会把 preamble 同时编进 000000.o 和（由 _cgo_export.c 生成的）000001.o
//   ⇒ ObjC 类被定义两次 ⇒ 链接期 duplicate symbol（CI 上真实踩到过）
//   ⇒ .m 文件由 cgo 只编译一次，天然无此问题，也无需 -x objective-c。
//
// ★ 所有函数必须在【主线程】调用（AppKit 的硬性要求）。
//   run.go 里 darwin 走的正是主线程分支。

#import <Cocoa/Cocoa.h>
#import <dispatch/dispatch.h>

// ── Go 侧导出、供 ObjC 调用的回调（在 tray_darwin.go 里 //export） ──
extern void goTrayShow(void);
extern void goTrayStop(void);
extern void goTrayQuit(void);
extern void goTrayReady(void);

// ★ NSStatusItem 的 button 方法没有出现在公开头文件里，
//   必须自己补 category，否则编译报
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

// ── 在主线程创建状态栏项；可从任意线程调用 ──
void xjTrayInstall(const char *tooltip,
                   const unsigned char *pngBytes, int pngLen, double ptSize)
{
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
                    gIcon = img;
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
                    [btn setTitle:@"T"];
                }
                // ★ 用 button.toolTip：NSStatusItem.setToolTip: 自 macOS 10.14 起已废弃，
                //   在 CI 上会被打成 ##[error]，干扰排错。
                if (tip != nil) {
                    [btn setToolTip:tip];
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

            [tip release];

            goTrayReady();
        }
    });
}

// ── 跑 AppKit 事件循环（阻塞；必须在主线程调用）──
void xjTrayRun(void)
{
    @autoreleasepool {
        NSApplication *app = [NSApplication sharedApplication];
        // Accessory ⇒ 不出现在 Dock、不抢焦点，只留状态栏图标（等价 LSUIElement）
        [app setActivationPolicy:NSApplicationActivationPolicyAccessory];
        [app run];
    }
}

// ── 让 [NSApp run] 返回（可从任意线程安全调用）──
void xjTrayStop(void)
{
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
