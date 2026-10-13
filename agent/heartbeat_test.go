package agent

import (
	"os"
	"strings"
	"testing"
	"time"
)

func TestAgentHeartbeatAlignedWithServerWindow(t *testing.T) {
	// 与 server/internal/logic/tunnel.PingInterval / ReadIdleTimeout 语义对齐。
	if agentPingInterval != 10*time.Second {
		t.Fatalf("agentPingInterval=%s，期望 10s（与服务端 PingInterval 一致）", agentPingInterval)
	}
	if agentReadIdleTimeout != 90*time.Second {
		t.Fatalf("agentReadIdleTimeout=%s，期望 90s（与服务端 ReadIdleTimeout 一致）", agentReadIdleTimeout)
	}
	if agentReadIdleTimeout < 2*agentPingInterval {
		t.Fatal("读空闲窗应覆盖至少两次 ping，避免偶丢一次就误杀")
	}
	if agentPongTimeout <= agentPingInterval {
		t.Fatalf("pongTimeout(%s) 必须 > ping(%s)", agentPongTimeout, agentPingInterval)
	}
}

// TestSoftRecycleConfig 软轮换必须【默认关闭】。
//
// 历史：某些链路（本机 Clash TUN 用 gvisor 用户态栈）每 ~40s 掐长连接，
// 于是客户端在 35s 主动 make-before-break 换连接绕开它。但轮换本身就是
// 「周期性重连」，用户看到的是「每 35s 断一下」—— 那是权宜之计，不是长连接。
// 正解是修链路（Clash TUN stack → system），所以默认值必须是 0（关闭）。
// 仍保留 -recycle 供改不了链路的机器显式打开（届时阈值须 < 40s）。
func TestSoftRecycleConfig(t *testing.T) {
	// 默认值由 main.go 的 -recycle flag 决定，必须是 0。
	src, err := os.ReadFile("../../main.go")
	if err != nil {
		t.Skipf("读不到 main.go：%v", err)
	}
	if !strings.Contains(string(src), `flag.Duration("recycle", 0,`) {
		t.Fatal("-recycle 的默认值必须是 0（默认不轮换、保持长连接）")
	}
	if agentPingInterval >= 20*time.Second {
		t.Fatalf("agentPingInterval=%s 必须明显密于旧 20s（否则又踩 2×ping=40s）", agentPingInterval)
	}
}

func TestBackoffCapsAt30s(t *testing.T) {
	if backoff(1) != time.Second {
		t.Fatalf("第 1 次应为 1s，得 %s", backoff(1))
	}
	if backoff(2) != 2*time.Second {
		t.Fatalf("第 2 次应为 2s，得 %s", backoff(2))
	}
	if backoff(3) != 4*time.Second {
		t.Fatalf("第 3 次应为 4s，得 %s", backoff(3))
	}
	if backoff(10) != 30*time.Second {
		t.Fatalf("应封顶 30s，得 %s", backoff(10))
	}
}
