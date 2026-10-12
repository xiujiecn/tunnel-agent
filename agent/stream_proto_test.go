package agent

import "testing"

// TestEffectiveStreamProtoFallsBackToAgentProto 帧 proto 空时必须回退 -proto。
//
// ★ 否则 -proto tcp 的 agent 会误走 HTTP-WS 且不发 streamOpenResult，
//
//	服务端 WaitStreamOpen 超时 → 公网 recv=b''。
func TestEffectiveStreamProtoFallsBackToAgentProto(t *testing.T) {
	if got := effectiveStreamProto("", "tcp"); got != "tcp" {
		t.Fatalf("空帧 proto + agent=tcp ⇒ tcp，got %q", got)
	}
	if got := effectiveStreamProto("", "udp"); got != "udp" {
		t.Fatalf("空帧 proto + agent=udp ⇒ udp，got %q", got)
	}
	if got := effectiveStreamProto("tcp", "http"); got != "tcp" {
		t.Fatalf("帧 proto 优先，got %q", got)
	}
	if got := effectiveStreamProto("", ""); got != "http" {
		t.Fatalf("双空应 Normalize 为 http，got %q", got)
	}
}

// TestDestructiveEffectiveProtoEmptyMustNotStayHTTPWhenAgentTCP 破坏性：
// 若回退被删掉（空帧永远当 http），本测试红。
func TestDestructiveEffectiveProtoEmptyMustNotStayHTTPWhenAgentTCP(t *testing.T) {
	got := effectiveStreamProto("", "tcp")
	if got != "tcp" {
		t.Fatalf("破坏性命中：空帧+agent=tcp 得到 %q（应 tcp）。"+
			"删除 effectiveStreamProto 回退即复现公网 recv 空", got)
	}
	isRaw := got == "tcp" || got == "udp"
	if !isRaw {
		t.Fatal("破坏性：未走 raw 路径 ⇒ 不会发 streamOpenResult")
	}
}
