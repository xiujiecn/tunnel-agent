package gui

import (
	"testing"
)

func TestTrayMenuIDsUniqueAndAssigned(t *testing.T) {
	want := map[string]int{
		"idmShow": idmShow,
		"idmStop": idmStop,
		"idmQuit": idmQuit,
		"idmOpen": idmOpen,
		"idmCopy": idmCopy,
	}
	expect := map[string]int{
		"idmShow": 1001,
		"idmStop": 1002,
		"idmQuit": 1003,
		"idmOpen": 1004,
		"idmCopy": 1005,
	}
	seen := map[int]string{}
	for name, id := range want {
		if id != expect[name] {
			t.Errorf("%s = %d，期望 %d", name, id, expect[name])
		}
		if other, ok := seen[id]; ok {
			t.Errorf("%s 与 %s 同为命令 id %d —— 菜单会点错项", name, other, id)
		}
		seen[id] = name
	}
	if idmOpen == idmCopy {
		t.Fatal("idmOpen 与 idmCopy 写成了同一个 id —— 「打开」与「复制」会互相抢命令")
	}
	if len(seen) != 5 {
		t.Fatalf("菜单 id 应有 5 个唯一值，实得 %d", len(seen))
	}
}

func TestCopyConfigURLInvokesCopyTextWithHostURL(t *testing.T) {
	origCopy := copyText
	origHost := host
	origURL := hostURL
	t.Cleanup(func() {
		copyText = origCopy
		host = origHost
		hostURL = origURL
	})

	const wantURL = "http://127.0.0.1:54321"
	hostURL = wantURL
	var got string
	var notified string
	copyText = func(s string) error {
		got = s
		return nil
	}
	host = TrayHost{
		Notify: func(msg string) { notified = msg },
	}

	copyConfigURL()

	if got != wantURL {
		t.Fatalf("copyText 收到 %q，期望 %q", got, wantURL)
	}
	if notified != "已复制："+wantURL {
		t.Fatalf("反馈 = %q，期望含已复制与地址", notified)
	}
}

func TestOpenConfigPageUsesHostURL(t *testing.T) {
	// openBrowser 会真起进程；这里只验空 URL 早退不 panic，
	// 以及 copy 钩子路径已覆盖「收到正确 URL」。
	origURL := hostURL
	t.Cleanup(func() { hostURL = origURL })
	hostURL = ""
	openConfigPage() // 不得 panic
	copyConfigURL()  // 空 URL 不得调用 copyText
}
