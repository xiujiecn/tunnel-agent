package lib

import (
	"errors"
	"testing"
)

func TestParseCommandLine(t *testing.T) {
	line := `tunnel-agent -server wss://tool.xiujie.cn/api/v1/tunnel/agent/attach -token abc123 -local 127.0.0.1:8092 -v`
	p, err := ParseCommandLine(line)
	if err != nil {
		t.Fatal(err)
	}
	if p.ServerURL != "wss://tool.xiujie.cn/api/v1/tunnel/agent/attach" {
		t.Fatalf("server=%q", p.ServerURL)
	}
	if p.Token != "abc123" {
		t.Fatalf("token=%q", p.Token)
	}
	if p.LocalAddr != "127.0.0.1:8092" || !p.Verbose {
		t.Fatalf("local=%q verbose=%v", p.LocalAddr, p.Verbose)
	}
}

func TestParseCommandLineMissingToken(t *testing.T) {
	_, err := ParseCommandLine(`tunnel-agent -server wss://x`)
	if !errors.Is(err, ErrMissingToken) {
		t.Fatalf("err=%v", err)
	}
}

func TestParseFields(t *testing.T) {
	p, err := ParseFields("wss://s", "t", "", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if p.LocalAddr != "127.0.0.1:3000" {
		t.Fatalf("default local=%q", p.LocalAddr)
	}
	if p.Proto != "http" {
		t.Fatalf("默认 proto 应为 http，得到 %q", p.Proto)
	}
}

// ★ 回归：下载页生成的启动命令带 -proto，GUI 的粘贴解析必须认。
// 曾经不认 ⇒ 整行一贴就报「未知参数 "-proto"」，GUI 里起不了 TCP 隧道。
func TestParseCommandLineWithProto(t *testing.T) {
	for _, tc := range []struct{ proto, want string }{
		{"tcp", "tcp"},
		{"udp", "udp"},
		{"http", "http"},
		{"TCP", "tcp"}, // 大小写不敏感
	} {
		line := `tunnel-agent -server wss://x/attach -token tk -local 127.0.0.1:8092 -proto ` + tc.proto
		p, err := ParseCommandLine(line)
		if err != nil {
			t.Fatalf("proto=%s 解析失败：%v", tc.proto, err)
		}
		if p.Proto != tc.want {
			t.Fatalf("proto=%s 得到 %q，期望 %q", tc.proto, p.Proto, tc.want)
		}
	}
}

func TestParseCommandLineProtoErrors(t *testing.T) {
	// 值非法要报错，不能静默兜底成 http
	if _, err := ParseCommandLine(`tunnel-agent -server wss://x -token t -proto tcpp`); err == nil {
		t.Fatal("非法 -proto 值应当报错")
	}
	// 缺值也要报错
	if _, err := ParseCommandLine(`tunnel-agent -server wss://x -token t -proto`); err == nil {
		t.Fatal("只写 -proto 不跟值应当报错")
	}
}
