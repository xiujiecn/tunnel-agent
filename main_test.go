package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	runtimepkg "runtime"
	"strings"
	"testing"
)

// TestLocalClientHasNoProxy 本地转发的 client 不带任何代理。
//
// ★ 这不是"防回环被劫持"——实测 Go 的 http.ProxyFromEnvironment
//
//	**本来就会绕过回环地址**（对 127.0.0.1 返回 nil）。
//	真正的理由是 **-local 允许填私有网段**：局域网里另一台机器的
//	开发服务是正当用法，而私有网段不在回环豁免范围内，
//	设了 HTTP_PROXY 的机器（公司网关 / 容器 / CI）会把它发给代理 ——
//	既失败（代理不认识该主机名），也等于把内网请求委托给了第三方。
//
// ★ 断言方式：直接检查 Proxy 为 nil。
//
//	★ 刻意**不用**"设 HTTP_PROXY 再发请求"那种间接写法：
//	那样断言的是 ProxyFromEnvironment 的行为（它自己会绕过回环），
//	把 Proxy 改回 ProxyFromEnvironment 时测试**依然会绿** ——
//	即它抓不到真正的回归。直接断言字段才能锁住这一行。
func TestLocalClientHasNoProxy(t *testing.T) {
	tr, ok := newLocalClient().Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport 类型不对，应为 *http.Transport")
	}
	if tr.Proxy != nil {
		t.Fatal("localClient 的 Proxy 必须是 nil：内网地址（含 -local 填私有网段的情形）不该被环境代理代转")
	}
}

// TestLocalClientIsReused 转发 client 必须复用，否则连接池每次都丢。
//
// ★ 为什么值得断言：每次请求新建 client = 新建 Transport = 连接池归零，
//
//	页面几十个静态资源就会把本地服务打成几十次三次握手，
//	而 Vite dev / Spring Boot debug 这类服务的 listen backlog 很小，
//	表现是"资源一多就 Connection refused"。
func TestLocalClientIsReused(t *testing.T) {
	rt := &runtime{localClient: newLocalClient()}
	if rt.localClient == nil {
		t.Fatal("localClient 不能为 nil：否则转发会退回 http.DefaultClient（走代理）")
	}
	// 同一个 client 上连发两次，第二次应复用第一次建立的连接。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = io.WriteString(w, "ok")
	}))
	defer srv.Close()
	for i := 0; i < 2; i++ {
		req, err := http.NewRequest(http.MethodGet, srv.URL, nil)
		if err != nil {
			t.Fatalf("第 %d 次构造请求失败：%v", i+1, err)
		}
		resp, err := rt.localClient.Do(req)
		if err != nil {
			t.Fatalf("第 %d 次请求失败：%v", i+1, err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		_ = resp.Body.Close()
	}
	// 断言 Transport 层确实配了连接池（MaxIdleConnsPerHost > 0）。
	tr, ok := rt.localClient.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("Transport 类型是 %T，应为 *http.Transport", rt.localClient.Transport)
	}
	if tr.MaxIdleConnsPerHost <= 0 {
		t.Fatal("MaxIdleConnsPerHost 必须为正：否则每个请求都要重新三次握手")
	}
}

// TestNewLocalClientHasNoTimeout 长连接（SSE / 长轮询）不能被客户端超时砍断。
//
// ★ 设了整体 Timeout 的话，一个 60 秒才吐一次的 SSE 流会在 30 秒被客户端
//
//	单方面中断，而服务端那头毫不知情 —— 表现为"页面刚开始加载就断"。
func TestNewLocalClientHasNoTimeout(t *testing.T) {
	if newLocalClient().Timeout != 0 {
		t.Fatal("localClient 不能设整体 Timeout：SSE / 长轮询会被客户端单方面砍断")
	}
}

// TestRequestBodyRegisteredBeforeGoroutine 请求体中转缓冲必须在起协程前登记。
//
// ★ 这是一条真实竞态的回归测试（症状：GET 正常，POST/PUT 永远收不到 body）：
//
//	读循环里`go rt.handleRequest(...)`，而 handleRequest 内部才
//	`pendingBodies.Store(...)`。新协程何时开跑是不确定的，
//	而服务端紧跟着发来的 reqBody 帧是在**当前读循环里同步处理**的：
//	    收到 request 帧 → 启动协程 → 继续读
//	    收到 reqBody 帧 → 查表 → **还没登记** → 静默丢弃
//	⇒ agent 日志里只印一行"→ POST /xxx"，看起来一切正常；
//	  本地服务则是"收到请求但 body 为空"。
//
// ★ 断言方式：直接检查"登记发生在调用 handleRequest 之前"这个结构事实。
//
//	行为层面（真发一个带 body 的请求）由端到端验证覆盖 ——
//	单测里复现它需要构造完整的服务端帧序列，代价远大于收益，
//	而"协程内登记"这个写法本身就是可以直接断言的。
func TestRequestBodyRegisteredBeforeGoroutine(t *testing.T) {
	// ★ 用 runtime.Caller 定位同目录的 main.go ——
	//   写死相对路径只在从仓库根跑时成立，而 `go test ./cmd/tunnel-agent/`
	//   会在**包目录**下跑（实测直接 Skip，等于这条测试形同虚设）。
	_, self, _, ok := runtimepkg.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller 失败，无法定位本测试文件")
	}
	src := filepath.Join(filepath.Dir(self), "main.go")
	raw, err := os.ReadFile(src)
	if err != nil {
		t.Fatalf("读不到 %s：%v", src, err)
	}
	content := string(raw)

	// 定位 frameRequest 的处理分支（启动 handleRequest 的地方）。
	idx := strings.Index(content, "case frameRequest:")
	if idx < 0 {
		t.Fatal("源码里找不到 case frameRequest: 分支")
	}
	branch := content[idx:]

	// 取出 `go rt.handleRequest(...)` 之前的那段（同一个 case 内）。
	goIdx := strings.Index(branch, "go rt.handleRequest(")
	if goIdx < 0 {
		t.Fatal("case frameRequest 里找不到 go rt.handleRequest(...)")
	}
	beforeGo := branch[:goIdx]

	// ★ 核心断言：登记动作必须在 go 语句**之前**。
	if !strings.Contains(beforeGo, "pendingBodies.Store(") {
		t.Fatal("pendingBodies.Store 必须出现在 `go rt.handleRequest(...)` 之前：" +
			"否则 reqBody 帧会在协程开跑前就到达、被静默丢弃（GET 正常但 POST 收不到 body）")
	}
	// 且 handleRequest 内部不应再自行登记（否则两份 pipe，后一份覆盖前一份）。
	rest := content[strings.Index(branch, "go rt.handleRequest("):]
	hIdx := strings.Index(rest, "func (rt *runtime) handleRequest(")
	if hIdx < 0 {
		t.Fatal("找不到 handleRequest 的定义")
	}
	hBody := rest[hIdx:]
	if hEnd := strings.Index(hBody, "\n}\n"); hEnd > 0 {
		hBody = hBody[:hEnd]
	}
	if strings.Contains(hBody, "pendingBodies.Store(") {
		t.Fatal("handleRequest 内部不应再 Store 请求体缓冲：调用方已登记，" +
			"这里再存一次会覆盖掉调用方那个 pipe")
	}
}
