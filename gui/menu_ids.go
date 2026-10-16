package gui

// 托盘右键菜单命令 ID（Windows）。
// ★ 放在无 build tag 的文件里，便于任意平台的单测钉住「唯一且不冲突」。
const (
	idmShow = 1001 // 显示窗口
	idmStop = 1002 // 停止全部
	idmQuit = 1003 // 退出
	idmOpen = 1004 // 直接打开配置页
	idmCopy = 1005 // 复制配置页地址
)
