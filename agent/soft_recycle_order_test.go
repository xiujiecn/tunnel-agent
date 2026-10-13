package agent

import (
	"os"
	"strings"
	"testing"
)

// TestSoftRecycleArmsBeforeDial ★ 回归守卫：软轮换的「置位」必须早于「拨号」。
//
// 为什么这是个真 bug（用户实测：客户端每 35s 断一下，日志里
// 「软轮换：本连接已满 35s…」之后没有「软轮换切换完成」，而是
// 「隧道已断开（… close 4012: replaced …），1s 后重连」）：
//
//	dialWS 的 WS 握手**本身就是 attach** —— 它成功返回的那一刻，
//	服务端已经接管本隧道并主动关掉了旧连接。
//	而旧连接被关闭时，读循环怎么判定，取决于 softArmed 有没有置位：
//	    已置位  ⇒ 认成「自己人轮换」⇒ 切到新连接（无缝）
//	    未置位  ⇒ 认成「真实断开」  ⇒ 打那句 + 1s 后重连
//	旧连接在 dialWS 期间就被服务端关掉了，若置位发生在 dialWS 之后，
//	读循环就会落进「真实断开」分支；更糟的是这次重连会再拨一条连接，
//	把刚拨好的新连接顶掉（服务端同样 4012）⇒ 变成来回抖动的重连。
//
// 这是时序竞态，纯单测很难稳定复现（取决于调度与 RTT），所以按本仓既有做法
// （见 fatal_test.go 对 classifyServeError 的文本断言）做源码级守卫。
func TestSoftRecycleArmsBeforeDial(t *testing.T) {
	src, err := os.ReadFile("core.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)

	arm := strings.Index(text, "softArmed.Store(true)")
	dial := strings.Index(text, "newConn, dialErr := rt.dialWS(ctx)")
	if arm < 0 || dial < 0 {
		t.Fatalf("找不到置位或拨号语句（arm=%d dial=%d）", arm, dial)
	}
	if arm > dial {
		t.Fatal("softArmed.Store(true) 必须早于 dialWS：dialWS 成功即代表服务端已接管并关掉旧连接")
	}

	// 拨号失败分支必须把语义恢复成「真实断开」，否则真断连会被当成轮换吞掉。
	if !strings.Contains(text, "softArmed.Store(false)") {
		t.Fatal("软轮换 Dial 失败分支缺少 softArmed.Store(false)")
	}

	// 读循环必须等拨号落位，不能立刻返回 errSoftRecycle ——
	// 那会再拨一条连接，把正在拨号的那条顶掉。
	if !strings.Contains(text, "<-handoffReady") {
		t.Fatal("读循环必须等 handoffReady 再决定切换/重连")
	}
}
