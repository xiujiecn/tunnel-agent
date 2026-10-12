package agent

import (
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

// TestSoftRecycleBeforePathKill 软轮换必须早于实测链路掐断（~40s）。
func TestSoftRecycleBeforePathKill(t *testing.T) {
	if softRecycleAfter <= 0 {
		t.Fatal("softRecycleAfter 必须开启（破坏性验证可临时改 0 复现 40s 断）")
	}
	if softRecycleAfter >= 40*time.Second {
		t.Fatalf("softRecycleAfter=%s 必须 < 40s（链路掐断实测阈值）", softRecycleAfter)
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
