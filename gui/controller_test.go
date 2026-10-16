package gui

import (
	"strings"
	"testing"
	"time"

	"github.com/xiujiecn/tunnel-agent/agent"
)

func TestMultiTunnelStopOneKeepsOther(t *testing.T) {
	ctrl := NewController()
	id1, err := ctrl.Add(agent.Config{
		ServerURL: "wss://127.0.0.1:9/nope-a",
		Token:     "token-aaaa-bbbb-cccc-dddd",
		LocalAddr: "127.0.0.1:18081",
		Proto:     "http",
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	id2, err := ctrl.Add(agent.Config{
		ServerURL: "wss://127.0.0.1:9/nope-b",
		Token:     "token-eeee-ffff-gggg-hhhh",
		LocalAddr: "127.0.0.1:18082",
		Proto:     "tcp",
	}, true)
	if err != nil {
		t.Fatal(err)
	}

	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if ctrl.RunningCount() >= 2 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if ctrl.RunningCount() < 2 {
		t.Fatalf("期望两条都 running，got count=%d list=%v", ctrl.RunningCount(), ctrl.List())
	}
	if n := len(ctrl.List()); n != 2 {
		t.Fatalf("List 应有 2 条，got %d", n)
	}

	if err := ctrl.Stop(id1); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		info1, ok1 := ctrl.Get(id1)
		info2, ok2 := ctrl.Get(id2)
		if ok1 && ok2 && !info1.Running && info2.Running {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	info1, _ := ctrl.Get(id1)
	info2, _ := ctrl.Get(id2)
	if info1.Running {
		t.Fatal("停 id1 后 id1 仍不应 running")
	}
	if !info2.Running {
		t.Fatal("停 id1 不得影响 id2（id2 必须仍 running）")
	}

	if err := ctrl.Remove(id1); err != nil {
		t.Fatal(err)
	}
	if n := len(ctrl.List()); n != 1 {
		t.Fatalf("Remove 后应剩 1 条，got %d", n)
	}
	if _, ok := ctrl.Get(id1); ok {
		t.Fatal("已删除的 id1 不应再出现")
	}
	if _, ok := ctrl.Get(id2); !ok {
		t.Fatal("id2 应仍在列表")
	}

	ctrl.StopAll()
}

func TestLogBuffersIsolatedPerTunnel(t *testing.T) {
	ctrl := NewController()
	id1, err := ctrl.Add(agent.Config{
		ServerURL: "wss://127.0.0.1:9/a",
		Token:     "tok1111222233334444",
		LocalAddr: "127.0.0.1:19001",
		Proto:     "http",
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	id2, err := ctrl.Add(agent.Config{
		ServerURL: "wss://127.0.0.1:9/b",
		Token:     "tok5555666677778888",
		LocalAddr: "127.0.0.1:19002",
		Proto:     "tcp",
	}, false)
	if err != nil {
		t.Fatal(err)
	}

	b1 := ctrl.LogsOf(id1)
	b2 := ctrl.LogsOf(id2)
	if b1 == nil || b2 == nil {
		t.Fatal("每条隧道必须有独立 LogBuffer")
	}
	if b1 == b2 {
		t.Fatal("两条隧道不得共用同一个 LogBuffer（否则日志会串）")
	}

	b1.Append("ONLY-TUNNEL-1")
	b2.Append("ONLY-TUNNEL-2")

	s1 := b1.Snapshot()
	s2 := b2.Snapshot()
	if !strings.Contains(s1, "ONLY-TUNNEL-1") || strings.Contains(s1, "ONLY-TUNNEL-2") {
		t.Fatalf("隧道1 日志串扰：%q", s1)
	}
	if !strings.Contains(s2, "ONLY-TUNNEL-2") || strings.Contains(s2, "ONLY-TUNNEL-1") {
		t.Fatalf("隧道2 日志串扰：%q", s2)
	}
}

func TestMaskToken(t *testing.T) {
	if got := maskToken("abcdefghijklmnop"); got != "abcd…mnop" {
		t.Fatalf("mask=%q", got)
	}
	if got := maskToken("short"); got != "********" {
		t.Fatalf("short mask=%q", got)
	}
}
