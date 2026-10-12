package agent

import (
	"testing"
	"time"
)

// TestHeartbeatTiming（P1）：心跳三参数必须满足"ping 显著小于等 pong 的上限"。
//
// ★ 为什么这条不变量重要（可运行证据见 TestPongTimeoutDetectsHalfDeadConnection）：
//
//	心跳闭环是「ping 发出 → 服务端回 pong → 本地收到」。
//	若 agentPongTimeout ≤ agentPingInterval，则上一发的监视还没等到 pong，
//	下一发又该发了 ⇒ 慢一点的网络就会**持续误判半死**，
//	表现为"隧道每隔 20s 自己断一次"—— 比原来的毛病更糟。
func TestHeartbeatTiming(t *testing.T) {
	if agentPingInterval != 10*time.Second {
		t.Fatalf("agentPingInterval=%s，期望 10s（须密于链路 ~40s 掐断）", agentPingInterval)
	}
	if agentPongTimeout <= agentPingInterval {
		t.Fatalf("agentPongTimeout=%s 必须**大于** agentPingInterval=%s —— "+
			"否则等 pong 的窗口比心跳周期还短，正常网络也会被误判半死",
			agentPongTimeout, agentPingInterval)
	}
	// 必须显著大于 ping 间隔（至少 1.5 倍），否则抖动余量不足。
	if agentPongTimeout < agentPingInterval+agentPingInterval/2 {
		t.Fatalf("agentPongTimeout=%s 对 agentPingInterval=%s 裕量不足（需 ≥1.5×）",
			agentPongTimeout, agentPingInterval)
	}
	if agentReadIdleTimeout < agentPongTimeout {
		t.Fatalf("agentReadIdleTimeout=%s 必须 ≥ agentPongTimeout=%s —— "+
			"读超时应该是「半死已被 pong 监视发现」之后的兜底，而不是更快的那个",
			agentReadIdleTimeout, agentPongTimeout)
	}
}

// TestBackoffResetsAfterHealthyConnection（P1 · 卡死态根因之一）
//
// ★ 复现的缺陷：run() 里的 attempt **只增不减**。
//
//	旧行为：连接稳定跑了很久 → 某次抖动 → attempt 仍停在高位
//	      ⇒ 此后每次重连都等 30s 封顶；"连上就断"的链路上更严重
//	      ⇒ 空窗占绝大多数时间，用户看到"隧道几乎永远 500"。
//
// ★ 直接测**生产函数** nextAttemptAfterDisconnect（不是复刻一份逻辑）——
//
//	复刻的测试测不到真实代码，改坏了也不会红（破坏性验证已实测过这个坑）。
func TestBackoffResetsAfterHealthyConnection(t *testing.T) {
	cases := []struct {
		name      string
		before    int
		aliveFor  time.Duration
		wantAfter int // nextAttemptAfterDisconnect 的返回值（复位后应为 0）
	}{
		{"长命连接后抖动 → 复位", 12, 3 * time.Minute, 0},
		{"刚好超过一个心跳 → 复位", 7, 21 * time.Second, 0},
		{"恰好等于一个心跳 → 不复位", 7, agentPingInterval, 7},
		{"短命连接 → 不复位（防重连风暴）", 12, 3 * time.Second, 12},
		{"秒断 → 不复位", 30, 500 * time.Millisecond, 30},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := nextAttemptAfterDisconnect(c.before, c.aliveFor)
			if got != c.wantAfter {
				t.Errorf("nextAttemptAfterDisconnect(attempt=%d, alive=%s) = %d，期望 %d",
					c.before, c.aliveFor, got, c.wantAfter)
			}
			// 复位后（返回 0）再 ++ 就是第 1 次重连 ⇒ 退避必须是 1s（快速恢复）。
			if c.wantAfter == 0 {
				if d := backoff(got + 1); d != time.Second {
					t.Errorf("复位后第一跳退避=%s，期望 1s —— 抖动后必须快速恢复", d)
				}
			}
		})
	}
}

// TestNextAttemptResetPreventsStuckAt30s（P1 · 卡死态的行为级判据）
//
// ★ 模拟真实序列："稳定跑很久 → 抖动 → 之后每次重连都必须快速尝试"。
//
//	旧逻辑下 attempt 停在高位 ⇒ 这一段全是 30s 封顶（用户视角：几乎永远 500）。
func TestNextAttemptResetPreventsStuckAt30s(t *testing.T) {
	attempt := 0
	// 先经历一次长命连接（3 分钟）。
	attempt = nextAttemptAfterDisconnect(attempt, 3*time.Minute)
	attempt++
	firstRetry := backoff(attempt)
	if firstRetry != time.Second {
		t.Fatalf("长命连接后的第一次重连退避=%s，期望 1s（实际会拖成 30s）", firstRetry)
	}
	// 随后连着 5 次短命连接（每次 2 秒就断）⇒ 退避应**逐级增长**（不��位）。
	delays := []time.Duration{}
	for i := 0; i < 5; i++ {
		attempt = nextAttemptAfterDisconnect(attempt, 2*time.Second)
		attempt++
		delays = append(delays, backoff(attempt))
	}
	// 每一次都必须 ≤ 30s（封顶），且整体不允许出现"永远 30s"。
	for i, d := range delays {
		if d > 30*time.Second {
			t.Errorf("第%d次短命连接后的退避=%s 超过封顶 30s", i+1, d)
		}
	}
	t.Logf("长命连接后首跳=%s，随后 5 次短命退避=%v", firstRetry, delays)
}

// TestBackoffStaysBoundedOnPersistentFailure（P1 · 防"重连风暴"）
//
// ★ 反向不变量：链路一直坏时，退避**必须**封顶在 30s，
//
//	否则"连上就断"会退化成每秒一次的连接风暴（打服务端、耗本地资源）。
func TestBackoffStaysBoundedOnPersistentFailure(t *testing.T) {
	if got := backoff(1000); got != 30*time.Second {
		t.Errorf("backoff(1000)=%s，期望封顶 30s", got)
	}
	if got := backoff(1); got != time.Second {
		t.Errorf("backoff(1)=%s，期望 1s", got)
	}
	// 阶梯必须单调不减（1,2,4,8,16,30,30…）。
	prev := time.Duration(0)
	for i := 1; i <= 10; i++ {
		d := backoff(i)
		if d < prev {
			t.Errorf("backoff(%d)=%s < backoff(%d)=%s，退阶必须单调不减",
				i, d, i-1, prev)
		}
		prev = d
	}
}
