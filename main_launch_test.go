package main

import (
	"testing"

	"github.com/xiujiecn/tunnel-agent/lib"
)

func TestLaunchModeCLIvsGUI(t *testing.T) {
	if lib.WantGUI(false, "", "") != true {
		t.Fatal("无参数应进 GUI")
	}
	if lib.WantGUI(false, "wss://x", "tok") != false {
		t.Fatal("完整参数应走 CLI")
	}
}
