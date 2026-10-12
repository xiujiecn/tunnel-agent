package gui

// TrayHost 系统托盘回调（显示控制台 / 停止隧道 / 退出进程）。
type TrayHost struct {
	Show   func()
	Stop   func()
	Quit   func()
	Notify func(string) // 托盘操作反馈（写入 GUI 日志区）；可空
}

// 托盘与宿主窗共享的运行态（各平台 runHostWindow / runTray 读写）。
var (
	host    TrayHost
	hostURL string
)
