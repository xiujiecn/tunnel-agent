package gui

// TrayHost 系统托盘回调（显示控制台 / 停止全部 / 退出进程）。
type TrayHost struct {
	Show   func()
	Stop   func() // 停止全部隧道（菜单文案「停止全部」）
	Quit   func()
	Notify func(string) // 托盘操作反馈（写入控制台伪日志）；可空
}

// 托盘与宿主窗共享的运行态（各平台 runHostWindow / runTray 读写）。
var (
	host    TrayHost
	hostURL string
)
