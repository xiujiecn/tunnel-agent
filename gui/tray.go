package gui

// TrayHost 系统托盘回调（显示控制台 / 停止隧道 / 退出进程）。
type TrayHost struct {
	Show func()
	Stop func()
	Quit func()
}
