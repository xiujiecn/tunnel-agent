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
	p, err := ParseFields("wss://s", "t", "", false)
	if err != nil {
		t.Fatal(err)
	}
	if p.LocalAddr != "127.0.0.1:3000" {
		t.Fatalf("default local=%q", p.LocalAddr)
	}
}
