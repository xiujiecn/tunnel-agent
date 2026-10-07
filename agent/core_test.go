package agent

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestLocalClientHasNoProxy(t *testing.T) {
	tr, ok := NewLocalClient().Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport 类型不对，应为 *http.Transport")
	}
	if tr.Proxy != nil {
		t.Fatal("localClient 的 Proxy 必须是 nil")
	}
}

func TestLocalClientIsReused(t *testing.T) {
	rt := &agentRuntime{localClient: NewLocalClient()}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()
	for i := 0; i < 2; i++ {
		req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
		if err != nil {
			t.Fatal(err)
		}
		resp, err := rt.localClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
	tr, ok := rt.localClient.Transport.(*http.Transport)
	if !ok || tr.MaxIdleConnsPerHost <= 0 {
		t.Fatal("连接池配置异常")
	}
}

func TestNewLocalClientHasNoTimeout(t *testing.T) {
	if NewLocalClient().Timeout != 0 {
		t.Fatal("localClient 不能设整体 Timeout")
	}
}

func TestRequestBodyRegisteredBeforeGoroutine(t *testing.T) {
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
	goIdx := strings.Index(branch, "go rt.handleRequest(")
	if goIdx < 0 {
		t.Fatal("找不到 go rt.handleRequest")
	}
	beforeGo := branch[:goIdx]
	if !strings.Contains(beforeGo, "pendingBodies.Store(") {
		t.Fatal("pendingBodies.Store 必须在 go handleRequest 之前")
	}
	if !strings.Contains(content, `req.Header.Del("Content-Length")`) {
		t.Fatal("handleRequest 必须剔 Content-Length，否则本地服务会按访客长度干等")
	}
	if !strings.Contains(content, "newPendingBody()") {
		t.Fatal("请求体必须经 newPendingBody（异步写），禁止在读循环里直接 pipe.Write")
	}
}
