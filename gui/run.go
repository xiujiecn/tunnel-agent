package gui

import (
	"context"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"sync"
	"syscall"
)

// Run 启动图形控制台（阻塞直到托盘「退出」或致命错误）。
func Run() error {
	ctrl := NewController()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	addr := ln.Addr().String()
	baseURL := "http://" + addr

	srv := newHTTPServer(ctrl, baseURL)
	go func() {
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			log.Printf("控制台 HTTP 服务异常：%v", err)
		}
	}()

	ctrl.logs.Append("图形控制台已就绪：" + baseURL)
	fmt.Fprintf(os.Stderr, "tunnel-agent 图形控制台：%s\n", baseURL)
	fmt.Fprintf(os.Stderr, "（无桌面环境时可在本机浏览器打开上述地址）\n")

	showUI := func() {
		_ = openBrowser(baseURL)
	}
	showUI()

	var wg sync.WaitGroup
	quit := make(chan struct{})
	var quitOnce sync.Once
	doQuit := func() {
		quitOnce.Do(func() {
			ctrl.Stop()
			_ = srv.Shutdown(context.Background())
			close(quit)
		})
	}

	host := TrayHost{
		Show: func() { showUI() },
		Stop: func() { ctrl.Stop() },
		Quit: doQuit,
	}

	runHostWindow(host, baseURL)

	if runtime.GOOS == "windows" {
		// Windows 托盘与宿主窗共用一条消息循环（同 OS 线程）。
		runTray(host)
		return nil
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		select {
		case <-sig:
			doQuit()
		case <-quit:
		}
	}()

	wg.Add(1)
	go func() {
		defer wg.Done()
		runTray(host)
	}()

	<-quit
	wg.Wait()
	return nil
}
