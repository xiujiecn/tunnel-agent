//go:build linux

package gui

import (
	"log"
	"os"
	"os/signal"
	"syscall"
)

func runTray(h TrayHost) {
	// ★ 纯 Go DBus AppIndicator 在各发行版差异大；首版降级为终端提示 + 信号退出，不 panic。
	log.Println("Linux 托盘：若桌面环境无 StatusNotifier，请使用浏览器控制台；Ctrl+C 退出。")
	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sig
		h.Quit()
	}()
}

func runHostWindow(h TrayHost, baseURL string) {
	_ = h
	_ = baseURL
}
