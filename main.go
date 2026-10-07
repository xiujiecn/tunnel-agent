// Command tunnel-agent —— 内网穿透的本地客户端（CLI + 图形控制台）。
package main

import (
	"context"
	"flag"
	"log"
	"os"
	"os/signal"
	"syscall"

	"github.com/xiujiecn/tunnel-agent/agent"
	"github.com/xiujiecn/tunnel-agent/gui"
	"github.com/xiujiecn/tunnel-agent/lib"
)

func main() {
	var (
		serverURL = flag.String("server", "", "服务端 agent 接入地址（ws:// 或 wss://）")
		token     = flag.String("token", "", "绑定票据（从秀杰工作台「内网穿透」页面获取）")
		local     = flag.String("local", "127.0.0.1:3000", "本地服务地址 host:port")
		verbose   = flag.Bool("v", false, "打印每条请求的转发明细")
		guiMode   = flag.Bool("gui", false, "强制打开图形控制台")
	)
	flag.Usage = func() { agent.Usage(os.Stderr) }
	flag.Parse()

	if lib.WantGUI(*guiMode, *serverURL, *token) {
		if err := gui.Run(); err != nil {
			log.Fatalf("图形控制台启动失败：%v", err)
		}
		return
	}

	if *serverURL == "" || *token == "" {
		flag.Usage()
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := agent.Run(ctx, agent.Config{
		ServerURL: *serverURL,
		Token:     *token,
		LocalAddr: *local,
		Verbose:   *verbose,
	}, nil); err != nil {
		log.Fatalf("隧道已断开：%v", err)
	}
}
