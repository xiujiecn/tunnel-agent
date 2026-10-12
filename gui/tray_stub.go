//go:build !windows && !linux && !darwin

package gui

import "log"

func runTray(h TrayHost) {
	log.Println("当前平台无系统托盘支持：请保持进程运行，在浏览器使用控制台；终止请按 Ctrl+C。")
	_ = h
}

func runHostWindow(h TrayHost, baseURL string) {
	host = h
	hostURL = baseURL
	log.Printf("配置页地址：%s（本平台无托盘菜单；请在浏览器打开）", baseURL)
}
