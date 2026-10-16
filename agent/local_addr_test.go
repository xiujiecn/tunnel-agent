package agent

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestParseLocalBaseURLSchemes(t *testing.T) {
	u, err := ParseLocalBaseURL("https://127.0.0.1:8443")
	if err != nil {
		t.Fatal(err)
	}
	if u.Scheme != "https" || u.Host != "127.0.0.1:8443" {
		t.Fatalf("got scheme=%q host=%q", u.Scheme, u.Host)
	}
	u2, err := ParseLocalBaseURL("127.0.0.1:8443")
	if err != nil {
		t.Fatal(err)
	}
	if u2.Scheme != "http" {
		t.Fatalf("无 scheme 应默认 http，got %q", u2.Scheme)
	}
}

func TestParseLocalBaseURLNeverNilNil(t *testing.T) {
	// TrimSpace 后合法 → 允许成功；非法 scheme / 空 → 必须 err；绝不能 (nil, nil)
	cases := []struct {
		in      string
		wantErr bool
	}{
		{"192.168.100.1:443 ", false}, // TrimSpace 后成功
		{"192.168.100.1:443\t", false},
		{"192.168.100.1:443\n", false},
		{"ftp://127.0.0.1:1", true},
		{"", true},
	}
	for _, tc := range cases {
		u, err := ParseLocalBaseURL(tc.in)
		if err == nil && u == nil {
			t.Fatalf("%q 返回了 (nil, nil)", tc.in)
		}
		if tc.wantErr && err == nil {
			t.Fatalf("%q 应报错", tc.in)
		}
		if !tc.wantErr && err != nil {
			t.Fatalf("%q 不该报错：%v", tc.in, err)
		}
	}
}

func TestCheckLocalAddrRejectsWhitespaceAndBadScheme(t *testing.T) {
	if err := CheckLocalAddr("192.168.100.1:443 "); err == nil {
		t.Fatal("尾随空格必须拒绝（与 url.Parse 口径对齐）")
	}
	if err := CheckLocalAddr("192.168.100.1:443\t"); err == nil {
		t.Fatal("尾随 tab 必须拒绝")
	}
	if err := CheckLocalAddr("https://127.0.0.1:8443"); err != nil {
		t.Fatalf("https 回环应通过：%v", err)
	}
	err := CheckLocalAddr("https://192.168.1.5:8443")
	var warning *warningError
	if !errors.As(err, &warning) {
		t.Fatalf("https 内网应 warning：%v", err)
	}
	if err := CheckLocalAddr("https://8.8.8.8:443"); err == nil {
		t.Fatal("公网 https 必须拒绝")
	}
	if err := CheckLocalAddr("ftp://127.0.0.1:1"); err == nil {
		t.Fatal("ftp scheme 必须拒绝")
	}
	if err := CheckLocalAddr("https://127.0.0.1:8443 "); err == nil {
		t.Fatal("https 尾随空白必须拒绝")
	}
	// Unicode 空白（NBSP）
	if err := CheckLocalAddr("127.0.0.1:\u00a03000"); err == nil {
		t.Fatal("含 NBSP 的地址必须拒绝")
	}
}

func TestCheckLocalAddrRejectsUserinfoBypass(t *testing.T) {
	// ★ P1：`https://127.0.0.1:3000@8.8.8.8` —— SplitHostPort 会看成回环，
	//   url.Parse 的 Host 却是公网 8.8.8.8。必须按最终 Hostname 拒绝 + 拒 userinfo。
	for _, in := range []string{
		"https://127.0.0.1:3000@8.8.8.8",
		"http://127.0.0.1:3000@8.8.8.8",
	} {
		if err := CheckLocalAddr(in); err == nil {
			t.Fatalf("%q 必须拒绝（公网绕过）", in)
		}
	}
	// ★ 回环 + userinfo：若只靠 Hostname 公网闸，这条例会放过 —— 必须显式拒 userinfo
	err := CheckLocalAddr("https://user:pass@127.0.0.1:8443")
	if err == nil {
		t.Fatal("https://user:pass@127.0.0.1:8443 必须拒绝")
	}
	if !strings.Contains(err.Error(), "userinfo") {
		t.Fatalf("应明确报 userinfo：%v", err)
	}
	if _, err := ParseLocalBaseURL("https://user:pass@127.0.0.1:8443"); err == nil {
		t.Fatal("ParseLocalBaseURL 也必须拒 userinfo")
	}
}

func TestCheckLocalAddrRejectsPathQueryFragment(t *testing.T) {
	for _, in := range []string{
		"http://127.0.0.1:3000/path",
		"https://127.0.0.1:8443/foo",
		"http://127.0.0.1:3000?x=1",
		"http://127.0.0.1:3000#frag",
	} {
		if err := CheckLocalAddr(in); err == nil {
			t.Fatalf("%q 必须拒绝（path/query/fragment）", in)
		}
	}
}

func TestRunRejectsExplicitBlankLocalAddr(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := Run(ctx, Config{
		ServerURL: "wss://127.0.0.1:9/nope",
		Token:     "tok",
		LocalAddr: "   ",
	}, nil)
	if err == nil {
		t.Fatal(`显式 -local " " 必须报错，不许静默换成 127.0.0.1:3000`)
	}
	if !strings.Contains(err.Error(), "空格") && !strings.Contains(err.Error(), "空白") {
		t.Fatalf("错误应指路空白：%v", err)
	}
}

func TestNormalizeLocalAddrInputTrimsThenOK(t *testing.T) {
	got, err := NormalizeLocalAddrInput("  127.0.0.1:3000  ")
	if err != nil {
		t.Fatal(err)
	}
	if got != "127.0.0.1:3000" {
		t.Fatalf("got %q", got)
	}
	got2, err := NormalizeLocalAddrInput("https://127.0.0.1:8443")
	if err != nil {
		t.Fatal(err)
	}
	if got2 != "https://127.0.0.1:8443" {
		t.Fatalf("got %q", got2)
	}
}

func TestRunRejectsIllegalLocalAddrWithoutPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("非法地址不得 panic：%v", r)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := Run(ctx, Config{
		ServerURL: "wss://127.0.0.1:9/nope",
		Token:     "tok",
		LocalAddr: "ftp://127.0.0.1:1",
	}, nil)
	if err == nil {
		t.Fatal("非法 scheme 必须返回 error")
	}
	if !strings.Contains(err.Error(), "本地地址不合法") && !strings.Contains(err.Error(), "http") {
		t.Fatalf("错误应指路：%v", err)
	}
}

func TestRunRejectsInternalWhitespaceWithoutPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("不得 panic：%v", r)
		}
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	// 中间空白：TrimSpace 去不掉 → 启动即拒
	err := Run(ctx, Config{
		ServerURL: "wss://127.0.0.1:9/nope",
		Token:     "tok",
		LocalAddr: "192.168.100.1 :443",
	}, nil)
	if err == nil {
		t.Fatal("含空白的地址必须启动报错")
	}
}

func TestHandleRequestNilLocalBaseNoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nil localBase 不得 panic：%v", r)
		}
	}()
	rt := &agentRuntime{localClient: NewLocalClient()}
	var mu sync.Mutex
	rt.handleRequest(nil, &mu, nil, frame{ID: "r1", Method: "GET", Path: "/"}, nil)
}

func TestOpenStreamNilLocalBaseNoPanic(t *testing.T) {
	defer func() {
		if r := recover(); r != nil {
			t.Fatalf("nil localBase 不得 panic：%v", r)
		}
	}()
	rt := &agentRuntime{localClient: NewLocalClient(), proto: "http"}
	var mu sync.Mutex
	var smu sync.Mutex
	rt.openStream(nil, &mu, &smu, map[string]net.Conn{}, nil, frame{StreamID: "s1", Proto: "http"})
}

func TestConnectOnceDoesNotDiscardLocalBaseURLError(t *testing.T) {
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("Caller 失败")
	}
	src, err := os.ReadFile(filepath.Join(filepath.Dir(self), "core.go"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(src)
	idx := strings.Index(content, "func (rt *agentRuntime) connectOnce")
	if idx < 0 {
		t.Fatal("找不到 connectOnce")
	}
	branch := content[idx:]
	end := strings.Index(branch, "\nfunc (")
	if end > 0 {
		branch = branch[:end]
	}
	if strings.Contains(branch, "localBase, _ := rt.localBaseURL()") {
		t.Fatal("connectOnce 不得丢弃 localBaseURL 错误（localBase, _ :=）—— 那会 SIGSEGV")
	}
	if !strings.Contains(branch, "localBase, err := rt.localBaseURL()") {
		t.Fatal("connectOnce 必须 localBase, err := rt.localBaseURL()")
	}
	if !strings.Contains(branch, "本地地址不合法") {
		t.Fatal("localBaseURL 出错时必须返回「本地地址不合法」")
	}
}

func TestBannerLineNeverEmptyBuildTime(t *testing.T) {
	line := BannerLine()
	if !strings.Contains(line, "tunnel-agent "+Version) {
		t.Fatalf("横幅缺版本：%q", line)
	}
	if !strings.Contains(line, "构建于") {
		t.Fatalf("横幅缺构建于：%q", line)
	}
	old := BuildTime
	BuildTime = ""
	defer func() { BuildTime = old }()
	line2 := BannerLine()
	if !strings.Contains(line2, "unknown") {
		t.Fatalf("空 BuildTime 必须显示 unknown：%q", line2)
	}
}
