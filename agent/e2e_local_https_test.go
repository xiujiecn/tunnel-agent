package agent

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

// 起一个假 attach：握手后发一条 GET，把 agent 回的响应 body 收集出来。
func startMockAttach(t *testing.T, onAgent func()) (attachURL string, gotBody *string, wait func()) {
	t.Helper()
	var bodyMu sync.Mutex
	body := ""
	gotBody = &body
	up := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	done := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		if onAgent != nil {
			onAgent()
		}
		req := frame{Type: frameRequest, ID: "r1", Method: "GET", Path: "/ping", Headers: map[string][]string{}}
		_ = conn.WriteMessage(websocket.TextMessage, mustJSON(req))
		// ★ GET 无 body 也必须发 reqEnd：agent 在 ID 非空时建了 pipe，等 EOF。
		_ = conn.WriteMessage(websocket.TextMessage, mustJSON(frame{Type: frameRequestEnd, ID: "r1"}))
		deadline := time.Now().Add(5 * time.Second)
		var chunks []byte
		for time.Now().Before(deadline) {
			_ = conn.SetReadDeadline(time.Now().Add(time.Second))
			_, payload, err := conn.ReadMessage()
			if err != nil {
				if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
					return
				}
				// 读超时继续等；真正断开则退出，避免 gorilla「repeated read」panic
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
			case frameResponseBody:
				b, _ := base64.StdEncoding.DecodeString(f.DataBase64)
				chunks = append(chunks, b...)
			case frameResponseEnd:
				bodyMu.Lock()
				body = string(chunks)
				bodyMu.Unlock()
				close(done)
				return
			case frameError:
				bodyMu.Lock()
				body = "ERR:" + f.Message
				bodyMu.Unlock()
				close(done)
				return
			}
		}
	}))
	t.Cleanup(srv.Close)
	u := "ws" + strings.TrimPrefix(srv.URL, "http")
	wait = func() {
		select {
		case <-done:
		case <-time.After(6 * time.Second):
			t.Fatal("等待 agent 响应超时")
		}
	}
	return u, gotBody, wait
}

func TestE2EHTTPForwardOneRequest(t *testing.T) {
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/ping" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, "PONG-HTTP")
	}))
	defer local.Close()
	host := strings.TrimPrefix(local.URL, "http://")

	attach, got, wait := startMockAttach(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errCh := make(chan error, 1)
	go func() {
		errCh <- Run(ctx, Config{ServerURL: attach, Token: "t", LocalAddr: host}, io.Discard)
	}()
	wait()
	cancel()
	if !strings.Contains(*got, "PONG-HTTP") {
		t.Fatalf("应转发到本地 HTTP，got %q", *got)
	}
}

func selfSignedTLSServer(t *testing.T, handler http.Handler) *httptest.Server {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "127.0.0.1"},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(24 * time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	tlsCert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewUnstartedServer(handler)
	srv.TLS = &tls.Config{Certificates: []tls.Certificate{tlsCert}}
	srv.StartTLS()
	t.Cleanup(srv.Close)
	return srv
}

func TestE2ELocalHTTPSSelfSignedWorks(t *testing.T) {
	httpsSrv := selfSignedTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "PONG-HTTPS")
	}))
	hostPort := strings.TrimPrefix(httpsSrv.URL, "https://")
	local := "https://" + hostPort

	attach, got, wait := startMockAttach(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = Run(ctx, Config{ServerURL: attach, Token: "t", LocalAddr: local}, io.Discard)
	}()
	wait()
	cancel()
	if !strings.Contains(*got, "PONG-HTTPS") {
		t.Fatalf("自签 https 应被本地一跳放行，got %q", *got)
	}
}

// TestDialLocalHTTPStreamHTTPSUpgradeSelfSigned 生产路径：自签 https 上做 WS Upgrade + 一帧往返。
//
// ★ 必须走 dialLocalHTTPStream（openStream 同款）；明文 TCP 打 TLS 端口会失败。
func TestDialLocalHTTPStreamHTTPSUpgradeSelfSigned(t *testing.T) {
	up := websocket.Upgrader{CheckOrigin: func(r *http.Request) bool { return true }}
	srv := selfSignedTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c, err := up.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer c.Close()
		mt, data, err := c.ReadMessage()
		if err != nil {
			return
		}
		_ = c.WriteMessage(mt, append([]byte("echo:"), data...))
	}))
	hostPort := strings.TrimPrefix(srv.URL, "https://")
	base, err := ParseLocalBaseURL("https://" + hostPort)
	if err != nil {
		t.Fatal(err)
	}

	dialer := websocket.Dialer{
		HandshakeTimeout: 5 * time.Second,
		// ws:// + 已握手的 TLS conn：避免 Dialer 再包一层 TLS
		NetDial: func(network, addr string) (net.Conn, error) {
			return dialLocalHTTPStream(base)
		},
	}
	ws, resp, err := dialer.Dial("ws://"+hostPort+"/", nil)
	if err != nil {
		t.Fatalf("自签 https Upgrade 必须成功（生产 dialLocalHTTPStream）：%v", err)
	}
	defer ws.Close()
	if resp.StatusCode != http.StatusSwitchingProtocols {
		t.Fatalf("期望 101，got %d", resp.StatusCode)
	}
	if err := ws.WriteMessage(websocket.TextMessage, []byte("ping")); err != nil {
		t.Fatal(err)
	}
	_, msg, err := ws.ReadMessage()
	if err != nil {
		t.Fatal(err)
	}
	if string(msg) != "echo:ping" {
		t.Fatalf("往返失败：%q", msg)
	}
}

func TestE2EHTTPSchemeAgainstHTTPSPortFails(t *testing.T) {
	httpsSrv := selfSignedTLSServer(t, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "SHOULD-NOT")
	}))
	hostPort := strings.TrimPrefix(httpsSrv.URL, "https://")
	// 故意用 http scheme 打 https 端口
	local := hostPort // 默认 http://

	attach, got, wait := startMockAttach(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		_ = Run(ctx, Config{ServerURL: attach, Token: "t", LocalAddr: local}, io.Discard)
	}()
	wait()
	cancel()
	if strings.Contains(*got, "SHOULD-NOT") || strings.Contains(*got, "PONG") {
		t.Fatalf("http 打 https 端口不该成功拿到正文，got %q", *got)
	}
	if !strings.HasPrefix(*got, "ERR:") && *got == "" {
		t.Fatalf("应失败（错误帧或空），got %q", *got)
	}
	fmt.Printf("http→https 对照失败形态：%q\n", *got)
}
