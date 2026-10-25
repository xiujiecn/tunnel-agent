package agent

import (
	"context"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// startHangTLSListener 起一个「能 TCP 连上、但永不完成 TLS 握手」的端口（模拟路由器管理页）。
func startHangTLSListener(t *testing.T) (addr string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			// 读一点但不回 ClientHello 应答 —— 对端握手一直等到 deadline。
			go func(conn net.Conn) {
				defer conn.Close()
				buf := make([]byte, 256)
				_ = conn.SetReadDeadline(time.Now().Add(30 * time.Second))
				_, _ = conn.Read(buf)
				time.Sleep(30 * time.Second)
			}(c)
		}
	}()
	return ln.Addr().String()
}

// TestLocalTLSHangDoesNotReconnect 本地「能连但 TLS 握手不回」：
//
//	请求拿到明确超时错误；整条隧道不得因这一条失败而重连（attach 只拨一次）。
//
// ★ 破坏性：若把 handleRequest 改回同步堵在读循环里，或超时后主动 Close WS，本测红。
func TestLocalTLSHangDoesNotReconnect(t *testing.T) {
	hangAddr := startHangTLSListener(t)
	local := "https://" + hangAddr

	oldDial, oldTLS := localDialTimeout, localTLSHandshakeTimeout
	localDialTimeout = 300 * time.Millisecond
	localTLSHandshakeTimeout = 300 * time.Millisecond
	t.Cleanup(func() {
		localDialTimeout = oldDial
		localTLSHandshakeTimeout = oldTLS
	})

	var dials atomic.Int32
	var (
		gotMu   sync.Mutex
		gotErr  string
		gotOnce sync.Once
		done    = make(chan struct{})
	)
	up := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		dials.Add(1)
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		req := frame{Type: frameRequest, ID: "r-tls", Method: "GET", Path: "/", Headers: map[string][]string{}}
		_ = conn.WriteMessage(websocket.TextMessage, mustJSON(req))
		_ = conn.WriteMessage(websocket.TextMessage, mustJSON(frame{Type: frameRequestEnd, ID: "r-tls"}))
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			_ = conn.SetReadDeadline(time.Now().Add(time.Second))
			_, payload, err := conn.ReadMessage()
			if err != nil {
				if ne, ok := err.(net.Error); ok && ne.Timeout() {
					continue
				}
				return
			}
			var f frame
			if json.Unmarshal(payload, &f) != nil {
				continue
			}
			switch f.Type {
			case frameError:
				gotMu.Lock()
				gotErr = f.Message
				gotMu.Unlock()
				gotOnce.Do(func() { close(done) })
				// 再挂一会儿：若 agent 因本地失败拆线重连，dials 会 >1。
				time.Sleep(800 * time.Millisecond)
				return
			case framePong, framePing:
				if f.Type == framePing {
					_ = conn.WriteMessage(websocket.TextMessage, mustJSON(frame{Type: framePong}))
				}
			}
		}
	}))
	t.Cleanup(srv.Close)
	attach := "ws" + strings.TrimPrefix(srv.URL, "http")

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- Run(ctx, Config{
			ServerURL: attach,
			Token:     "t",
			LocalAddr: local,
		}, io.Discard)
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("未在时限内收到本地 TLS 超时错误帧")
	}
	gotMu.Lock()
	msg := gotErr
	gotMu.Unlock()
	if !strings.Contains(msg, "local TLS handshake timeout") {
		t.Fatalf("错误应含 local TLS handshake timeout，得 %q", msg)
	}
	if !strings.Contains(msg, hangAddr) {
		t.Fatalf("错误应带目标地址 %s，得 %q", hangAddr, msg)
	}

	// 给重连一点窗口
	time.Sleep(500 * time.Millisecond)
	if n := dials.Load(); n != 1 {
		t.Fatalf("单次本地 TLS 失败不得触发隧道重连：attach dials=%d（期望 1）", n)
	}
	cancel()
	select {
	case <-errCh:
	case <-time.After(3 * time.Second):
	}
}

// TestDialLocalHTTPStreamTLSHangTimesOut 单元：握手不回 ⇒ 有界失败 + 可读文案。
func TestDialLocalHTTPStreamTLSHangTimesOut(t *testing.T) {
	hangAddr := startHangTLSListener(t)
	old := localTLSHandshakeTimeout
	localTLSHandshakeTimeout = 200 * time.Millisecond
	t.Cleanup(func() { localTLSHandshakeTimeout = old })

	base, err := ParseLocalBaseURL("https://" + hangAddr)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	_, err = dialLocalHTTPStream(base)
	elapsed := time.Since(started)
	if err == nil {
		t.Fatal("TLS 挂起应失败")
	}
	if !strings.Contains(err.Error(), "local TLS handshake timeout") {
		t.Fatalf("期望可读超时，得 %v", err)
	}
	if elapsed > 2*time.Second {
		t.Fatalf("握手超时应在短时内返回，实际 %s", elapsed)
	}
}

// TestHandleRequestNotBlockingReadLoop 源码：frameRequest 必须 go handleRequest。
//
// ★ 破坏性：改成同步 rt.handleRequest(...) ⇒ 本测红（本地 TLS 会拖死读循环）。
func TestHandleRequestNotBlockingReadLoop(t *testing.T) {
	_, self, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller 失败")
	}
	src, err := os.ReadFile(filepath.Join(filepath.Dir(self), "core.go"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(src)
	idx := strings.Index(content, "case frameRequest:")
	if idx < 0 {
		t.Fatal("找不到 case frameRequest:")
	}
	branch := content[idx:]
	if end := strings.Index(branch, "case frameRequestBody:"); end > 0 {
		branch = branch[:end]
	}
	if !strings.Contains(branch, "go rt.handleRequest(") {
		t.Fatal("handleRequest 必须在独立 goroutine，否则本地 TLS 会拖死 WS 读循环")
	}
	if strings.Contains(branch, "\n\t\t\trt.handleRequest(") {
		t.Fatal("禁止同步调用 handleRequest")
	}
}
