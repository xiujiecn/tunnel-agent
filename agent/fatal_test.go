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
		{"无效", "ticket invalid: 绑定凭证无效", 401, true, "ticket invalid"},
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
			if !strings.Contains(err.Error(), "换发启动命令") {
				t.Fatalf("致命错误应提示换发：%v", err)
			}
		})
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
