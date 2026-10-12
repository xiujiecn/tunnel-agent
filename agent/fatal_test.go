package agent

import (
	"errors"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func TestClassifyDialErrorTicketCases(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		status int
		fatal  bool
		want   string
	}{
		{"已吊销", "ticket revoked: 票据已作废，请到页面「换发启动命令」获取新命令", 401, true, "ticket revoked"},
		{"不匹配", "ticket mismatch: 票据与隧道不匹配", 401, true, "ticket mismatch"},
		{"隧道关闭", "tunnel closed: 隧道已关闭或不存在", 401, true, "tunnel closed"},
		{"无效", "ticket invalid: 绑定凭证无效", 401, true, AgentOutdatedHint},
		{"版本过旧", "ticket version obsolete: 请重新下载", 401, true, AgentOutdatedHint},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := &http.Response{
				StatusCode: tc.status,
				Body:       io.NopCloser(strings.NewReader(tc.body)),
			}
			err := classifyDialError(errors.New("bad handshake"), resp)
			if tc.fatal && !errors.Is(err, ErrFatalConnect) {
				t.Fatalf("应得 Fatal，实得 %v", err)
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%q 应含 %q", err, tc.want)
			}
			if strings.Contains(tc.body, "ticket invalid") || strings.Contains(tc.body, "ticket version obsolete") {
				if !strings.Contains(err.Error(), "重新下载") {
					t.Fatalf("版本/无效应提示重新下载：%v", err)
				}
			} else if !strings.Contains(err.Error(), "换发启动命令") {
				t.Fatalf("其它致命错误应提示换发：%v", err)
			}
		})
	}
}

func TestAttachURLCarriesAgentVer(t *testing.T) {
	u, err := attachURL("wss://tool.xiujie.cn/api/v1/tunnel/agent/attach", "tok", "http")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(u, "agentVer="+Version) {
		t.Fatalf("握手 URL 必须带 agentVer：%s", u)
	}
	if !strings.Contains(u, "ticket=tok") {
		t.Fatalf("握手 URL 必须带 ticket：%s", u)
	}
}

func TestRunStopsOnObsoleteWithoutReconnectLoop(t *testing.T) {
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("Caller 失败")
	}
	src, err := os.ReadFile(filepath.Join(filepath.Dir(self), "core.go"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	if !strings.Contains(text, "AgentOutdatedHint") {
		t.Fatal("致命退出必须区分「版本过旧」提示")
	}
	if !strings.Contains(text, "已停止重连") {
		t.Fatal("致命退出必须停止重连")
	}
}

func TestClassifyDialErrorTransientNetwork(t *testing.T) {
	err := classifyDialError(errors.New("dial tcp: connection refused"), nil)
	if errors.Is(err, ErrFatalConnect) {
		t.Fatalf("网络拒绝不应判致命：%v", err)
	}
}

func TestClassifyServeErrorCloseCodes(t *testing.T) {
	err := classifyServeError(&websocket.CloseError{Code: 4001, Text: "ticket revoked: 票据已作废"})
	if !errors.Is(err, ErrFatalConnect) {
		t.Fatalf("4001 应致命：%v", err)
	}
	err2 := classifyServeError(errors.New("与服务端的连接断开：unexpected EOF"))
	if errors.Is(err2, ErrFatalConnect) {
		t.Fatalf("裸 EOF 在无机器码时仍作瞬时（靠握手阶段拦截）：%v", err2)
	}
}

// TestAlreadyAttachedIsTransient 半开槽位未释放时重连撞 already attached，不得 fatal。
func TestAlreadyAttachedIsTransient(t *testing.T) {
	err := classifyServeError(&websocket.CloseError{
		Code: 4006,
		Text: "agent already attached: 这条隧道已有 agent 在运行",
	})
	if errors.Is(err, ErrFatalConnect) {
		t.Fatalf("4006 already attached 必须有界重试，不得 fatal：%v", err)
	}
	for _, code := range []int{4010, 4011, 4012} {
		e := classifyServeError(&websocket.CloseError{Code: code, Text: "heartbeat/replaced"})
		if errors.Is(e, ErrFatalConnect) {
			t.Fatalf("close %d 应为瞬时：%v", code, e)
		}
	}
	// 文案路径（无 close code 时）—— 不得再被 fatalCloseHints 命中。
	errText := classifyServeError(errors.New("agent already attached: 这条隧道已有 agent 在运行"))
	if errors.Is(errText, ErrFatalConnect) {
		t.Fatalf("文案 already attached 不得 fatal：%v", errText)
	}
}

// TestAlreadyAttachedDestructiveMustRedWhenFatalAgain 破坏性：若把 already attached 加回 fatal 列表，本测必红。
func TestAlreadyAttachedDestructiveMustRedWhenFatalAgain(t *testing.T) {
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("Caller 失败")
	}
	src, err := os.ReadFile(filepath.Join(filepath.Dir(self), "fatal.go"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(src)
	// fatalCloseHints 不得再含 already attached
	if i := strings.Index(text, "var fatalCloseHints"); i >= 0 {
		chunk := text[i:]
		if j := strings.Index(chunk, "}"); j > 0 {
			chunk = chunk[:j]
		}
		if strings.Contains(chunk, "agent already attached") {
			t.Fatal("fatalCloseHints 又把 already attached 当致命 —— 浸泡会再停连")
		}
	}
	if !strings.Contains(text, "case 4006, 4010, 4011, 4012:") {
		t.Fatal("classifyServeError 必须把 4006/4010–4012 当瞬时")
	}
}

func TestRunStopsOnFatal(t *testing.T) {
	// 结构守卫：run 循环必须检查 ErrFatalConnect。
	src, err := os.ReadFile("core.go")
	if err != nil {
		// go test 可能在包目录外；用 Caller 定位。
		_, self, _, ok := runtime.Caller(0)
		if !ok {
			t.Fatal(err)
		}
		src, err = os.ReadFile(filepath.Join(filepath.Dir(self), "core.go"))
		if err != nil {
			t.Fatal(err)
		}
	}
	text := string(src)
	if !strings.Contains(text, "ErrFatalConnect") {
		t.Fatal("run 必须识别 ErrFatalConnect 并停止重连")
	}
	if !strings.Contains(text, "已停止重连") {
		t.Fatal("致命退出必须打印可操作提示")
	}
}
