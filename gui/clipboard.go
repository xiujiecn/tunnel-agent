package gui

import "log"

// copyText 把文本写入系统剪贴板。
// ★ 做成可替换变量：生产默认走平台实现；单测注入假实现断言收到的 URL。
var copyText = copyTextDefault

func openConfigPage() {
	if hostURL == "" {
		return
	}
	if err := openBrowser(hostURL); err != nil {
		notifyTray("打开配置页失败：" + err.Error())
	}
}

func copyConfigURL() {
	if hostURL == "" {
		return
	}
	if err := copyText(hostURL); err != nil {
		notifyTray("复制失败：" + err.Error())
		return
	}
	notifyTray("已复制：" + hostURL)
}

func notifyTray(msg string) {
	log.Println(msg)
	if host.Notify != nil {
		host.Notify(msg)
	}
}
