package agent

import (
	"fmt"
	"io"
	"os"
)

// Version tunnel-agent 自报版本（握手 query agentVer=…）。
//
// ★ 服务端用它区分日志与兼容策略；改行为时请 bump 次版本号。
//
//	保持 const，避免与服务端兼容策略的取值口径因 ldflags 漂移。
const Version = "1.2.1"

// BuildTime 编译时间（由 -ldflags -X 注入；未注入时为 "unknown"）。
//
// ★ 必须是 var 才能被 ldflags 覆盖。
var BuildTime = "unknown"

// BannerLine 启动横幅 / -version 共用的一行文案。
func BannerLine() string {
	bt := BuildTime
	if bt == "" {
		bt = "unknown"
	}
	return fmt.Sprintf("tunnel-agent %s（构建于 %s）", Version, bt)
}

// PrintBanner 把横幅写到 w（nil → stderr）。
func PrintBanner(w io.Writer) {
	if w == nil {
		w = os.Stderr
	}
	fmt.Fprintln(w, BannerLine())
}
