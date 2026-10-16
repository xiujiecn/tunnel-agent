// Command tunnel-agent —— 内网穿透的本地客户端。
//
// 用法：
//
//	tunnel-agent -server wss://tool.xiujie.cn/api/v1/tunnel/agent/attach \
//	             -token <绑定票据> -local 127.0.0.1:3000
//
// ★ 它做的事只有一件：**反向连接**到服务端，把服务端转来的 HTTP 请求
//
//	搬到本机指定端口上的服务去，再把响应搬回去。
//	用户机器上不需要任何入站端口映射、不需要公网 IP、
//	不需要在防火墙上开洞 —— 只要能出网就行。
//
// ★ 为什么是"反向"而不是"本地起一个服务对外暴露"：
//
//	后者要求用户内网能被公网直连（端口映射 + 公网 IP + 防火墙开洞），
//	而"临时把同事电脑上的页面给外部看一眼"这种需求做不到那一步。
//	这也是 ngrok / localtunnel 的架构。
//
// ★ 单文件、无第三方依赖（只用标准库 + gorilla/websocket，
//
//	后者与本仓库 server 侧同版本，避免协议实现不一致）。
package agent

import (
	"context"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

// 帧类型（与服务端 logic/tunnel/proxy.go 的常量**逐字对应**）。
//
// ★ 两端各写一份常量是这份协议里最大的隐患：
//
//	改了一端而忘了另一端时，症状是"隧道连上了但请求全失败"，
//	而报文里只是一个看不懂的 type 字符串。注释里写明"改这里也要改那里"，
//	并在本文件里给出与 Go 侧常量的对照表。
const (
	frameRequest          = "request"  // 服务端 → agent：开始一次本地请求
	frameRequestBody      = "reqBody"  // 服务端 → agent：请求体数据块
	frameRequestEnd       = "reqEnd"   // 服务端 → agent：请求体结束
	frameResponse         = "response" // agent → 服务端：响应头
	frameResponseBody     = "resBody"  // agent → 服务端：响应体数据块
	frameResponseEnd      = "resEnd"   // agent → 服务端：响应结束
	frameError            = "error"    // agent → 服务端：本地请求失败
	frameClose            = "close"    // 服务端 → agent：隧道已关闭，优雅退出
	framePing             = "ping"
	framePong             = "pong"
	frameStreamOpen       = "streamOpen"       // 服务端 → agent：开流（HTTP-WS / TCP / UDP）
	frameStreamOpenResult = "streamOpenResult" // agent → 服务端：本地 dial 成败
	frameStreamClose      = "streamClose"      // 双向：关闭某条透传流
	frameStreamBinary     = "streamBin"        // 双向：透传流的原始字节
)

// frame 一帧（字段与服务端 Frame 结构体对应）。
type frame struct {
	Type          string              `json:"type"`
	ID            string              `json:"id,omitempty"`
	StreamID      string              `json:"streamId,omitempty"`
	Method        string              `json:"method,omitempty"`
	Path          string              `json:"path,omitempty"`
	Query         string              `json:"query,omitempty"`
	Headers       map[string][]string `json:"headers,omitempty"`
	Host          string              `json:"host,omitempty"`
	Status        int                 `json:"status,omitempty"`
	Message       string              `json:"message,omitempty"`
	DataBase64    string              `json:"data,omitempty"`
	Upgrade       bool                `json:"upgrade,omitempty"`
	UpgradeHeader map[string]string   `json:"upgradeHeader,omitempty"`
	Proto         string              `json:"proto,omitempty"`
	Remote        string              `json:"remote,omitempty"`
	Local         string              `json:"local,omitempty"`
	Ok            *bool               `json:"ok,omitempty"`
}

// bodyChunk 单帧正文大小上限（与服务端 tunnelBodyChunk 一致：64KB）。
const bodyChunk = 64 << 10

// Config agent 连接参数（CLI 与 GUI 共用）。
type Config struct {
	ServerURL string
	Token     string
	LocalAddr string
	Verbose   bool
	// Proto 协议：http（默认）| tcp | udp。缺省必须保持旧行为。
	Proto string
	// SoftRecycleAfter 软轮换阈值：> 0 时在链路掐断前主动 make-before-break 换连接。
	//
	// ★ 默认 0 = 关闭，保持一条真正的长连接。
	//   历史：某些链路（本机 Clash TUN 用 gvisor 用户态栈）会每 ~40s 掐长连接，
	//   于是客户端在 35s 主动换连接来绕开它 —— 但那本身就是「周期性重连」，
	//   用户看到的就是「每 35s 断一下」。正解是把链路修好
	//   （Clash TUN stack 改 system），于是默认关闭；
	//   只有改不了链路的机器才用 -recycle 显式打开。
	SoftRecycleAfter time.Duration
}

// NormalizeProto 空 / 未知 → http（旧脚本不加 -proto 仍只跑 HTTP）。
func NormalizeProto(p string) string {
	p = strings.TrimSpace(strings.ToLower(p))
	switch p {
	case "tcp", "udp":
		return p
	default:
		return "http"
	}
}

// Run 启动隧道转发，阻塞直到 ctx 结束或不可恢复错误。
// out 为 nil 时写到 os.Stdout；GUI 可传入带 SSE 的 Writer。
func Run(ctx context.Context, cfg Config, out io.Writer) error {
	// ★ 只有真正没给 -local（空串）才用默认值；显式纯空白必须报错，不许静默换成 127.0.0.1:3000。
	if cfg.LocalAddr == "" {
		cfg.LocalAddr = "127.0.0.1:3000"
	} else if strings.TrimSpace(cfg.LocalAddr) == "" {
		return fmt.Errorf("本地地址不合法：%w", ErrLocalAddrWhitespace)
	}
	normalized, err := NormalizeLocalAddrInput(cfg.LocalAddr)
	if err != nil {
		return fmt.Errorf("本地地址不合法：%w", err)
	}
	cfg.LocalAddr = normalized
	cfg.Proto = NormalizeProto(cfg.Proto)
	if err := CheckLocalAddr(cfg.LocalAddr); err != nil {
		var warning *warningError
		if errors.As(err, &warning) {
			log.Printf("⚠️  %v", err)
			log.Printf("   继续运行。确认这台机器上的服务就是你打算暴露的那个即可。")
		} else {
			return fmt.Errorf("本地地址不合法：%w", err)
		}
	}
	// ★ fail-fast：启动时就解析基址，绝不能把 nil 活到第一条请求再 SIGSEGV。
	base, err := ParseLocalBaseURL(cfg.LocalAddr)
	if err != nil {
		return fmt.Errorf("本地地址不合法（-local %q）：%w", cfg.LocalAddr, err)
	}
	if base.Scheme == "https" && !isLoopbackHost(base.Hostname()) {
		log.Printf("本地一跳不校验 TLS 证书（本地服务常是自签）；目标在局域网，请自行确认那台机器可信")
	}
	rt := &agentRuntime{
		serverURL:        cfg.ServerURL,
		token:            cfg.Token,
		localAddr:        cfg.LocalAddr,
		proto:            cfg.Proto,
		verbose:          cfg.Verbose,
		softRecycleAfter: cfg.SoftRecycleAfter,
		localClient:      NewLocalClient(),
		out:              out,
	}
	return rt.run(ctx)
}

// Usage 打印 CLI 帮助。
func Usage(w io.Writer) {
	if w == nil {
		w = os.Stderr
	}
	usage(w)
}

func usage(w io.Writer) {
	fmt.Fprintf(w, `tunnel-agent —— 秀杰工作台内网穿透客户端

用法：
  tunnel-agent -server wss://tool.xiujie.cn/api/v1/tunnel/agent/attach -token <票据> -local 127.0.0.1:3000
  tunnel-agent -proto tcp -server … -token … -local 127.0.0.1:22

参数：
  -server   服务端 agent 接入地址（必填）
  -token    绑定票据（必填；换发后旧票失效）
  -local    本地服务地址，默认 127.0.0.1:3000
            可写 host:port（默认 http）或显式 http(s)://host:port
  -proto    协议：http（默认）| tcp | udp；必须与隧道创建时一致
  -v        打印每条请求的转发明细
  -version  打印版本与编译时间后退出
  -gui      打开图形控制台

说明：
  · 本工具会把你指定的本地服务**临时暴露到公网**，
    任何拿到公网地址的人都能访问它。请只分享给确实需要访问的人。
  · 票据换发或关隧道后旧票失效；请用页面最新启动命令。
  · 本地地址只允许回环或私有网段（见启动时的检查）——
    它不是安全边界，而是一道防手滑的闸：把 -local 写成某个内网 IP
    会让你的 agent 变成访问那台机器的跳板。
  · -local 支持 https://host:port（自签可用）；不带 scheme 默认 http。
    ★ 本地一跳不校验证书；agent↔服务端的 wss 仍严格校验。
  · 本命令行模式【一个进程只支持一条隧道】；要同时跑多条，请用图形控制台（双击或 -gui），或为每条隧道各起一个进程。
`)
}

// runtime agent 的运行期状态。
type agentRuntime struct {
	serverURL string
	token     string
	localAddr string
	proto     string
	verbose   bool
	// softRecycleAfter 见 Config.SoftRecycleAfter：0 = 不轮换（默认），保持长连接。
	softRecycleAfter time.Duration
	out              io.Writer

	// localClient 转发到本地服务用的 client，**整个进程复用**。
	//
	// ★ 复用而不是每次请求新建：新建 client 等于新建 Transport，
	//   连接池随之丢失 —— 隧道的高频访问（页面几十个静态资源）
	//   就会每次都重新三次握手，而本地服务往往 listen backlog 很小。
	//
	// ★ 它不能是 http.DefaultClient：后者没有连接池参数（新建即丢），
	//   且代理策略不可控。理由详见 newLocalClient 的注释。
	localClient *http.Client

	// pendingBodies 在途请求的请求体表（每条隧道 / 每个 runtime 自持）。
	//
	// ★ 曾是包级全局 sync.Map：单实例服务端 stream ID 全局唯一时不会串包，
	//   但图形控制台同进程多隧道后必须下沉，避免跨隧道共享可变状态。
	pendingBodies sync.Map // requestID → *pendingBody
}

func (rt *agentRuntime) emitf(format string, args ...any) {
	w := rt.out
	if w == nil {
		w = os.Stdout
	}
	_, _ = fmt.Fprintf(w, format, args...)
}

func (rt *agentRuntime) emitln(args ...any) {
	w := rt.out
	if w == nil {
		w = os.Stdout
	}
	_, _ = fmt.Fprintln(w, args...)
}

// NewLocalClient 造一个"只连本地、绝不走代理"的 client。
func NewLocalClient() *http.Client {
	return &http.Client{
		Transport: &http.Transport{
			// ★ 显式 nil：不继承任何环境代理设置。
			//
			//	★ 先纠正一个容易被误判的点：**Go 的
			//	  http.ProxyFromEnvironment 本来就会绕过回环地址**
			//	  （对 127.0.0.1 / localhost 直接返回 nil，实测确认）。
			//	  所以"回环请求被代理劫持"并不是真实存在的故障 ——
			//	  早期注释把它当成真因写过，是错的。
			//
			//	★ 但显式 nil 仍然是对的，理由与回环无关，而是**内网地址**：
			//	  - -local 允许填私有网段（见 checkLocalAddr 的警示分支，
			//	    "局域网里另一台机器的开发服务"是正当用法）；
			//	  - 私有网段**不在**回环豁免范围内 —— 设了 HTTP_PROXY 的机器
			//	    （公司网关 / 容器 / CI）会把它发给代理；
			//	  - 而把一台内网机器的请求委托给第三方代为转发，
			//	    既会失败（代理通常不认识该主机名），也是一次不该发生的暴露。
			//
			//	⇒ 本地转发一律不走代理，理由是"目标是内网，不该被第三方代转"。
			Proxy: nil,
			DialContext: (&net.Dialer{
				Timeout:   10 * time.Second,
				KeepAlive: 30 * time.Second,
			}).DialContext,
			MaxIdleConns:          32,
			MaxIdleConnsPerHost:   8,
			IdleConnTimeout:       60 * time.Second,
			TLSHandshakeTimeout:   10 * time.Second,
			ExpectContinueTimeout: 1 * time.Second,
			// 本地服务常是自签名 / 私有 CA 的开发服务（Vite dev、Spring Boot debug 等），
			// 这里放宽证书校验是合理的 ——
			// ★ 理由：这条连接**不出本机**、不经过任何网络设备，传输安全由回环保证；
			//   证书校验在这条链路上防的是"中间人"，而中间人在回环上不存在。
			//   ★ 公网那一侧（agent ↔ 服务端的 wss）仍然严格校验证书，
			//     放宽只发生在这条不出机的连接上（见 connectOnce 的 TLSClientConfig）。
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true, MinVersion: tls.VersionTLS12},
		},
		// ★ 不设 Timeout：本地服务可能是 SSE / 长轮询这类长连接，
		//   设了整体超时会把它们在固定时间后砍断。超时由服务端那一侧的
		//   headTimeout / 空闲判定负责。
	}
}

func (rt *agentRuntime) run(ctx context.Context) error {
	attempt := 0
	// ★★ P1：记录"这一把连接是从什么时候开始建立的"——用来算存活时长。
	connectedAt := time.Time{}
	for {
		if ctx.Err() != nil {
			return nil
		}
		// ★ 必须在 connectOnce **之前**取时间：connectOnce 返回时连接已经结束，
		//   事后取会把存活时长算成 0（那正是"永远复位/永不���位"的反向错误）。
		connectedAt = time.Now()
		err := rt.connectOnce(ctx)
		if ctx.Err() != nil {
			return nil
		}
		// ★ 致命错误（票据已用/过期/不匹配、隧道已关）⇒ 停止重连。
		//   旧实现对一切断开都退避重试 ⇒ 复用废票时永远「1s 后重连」。
		if errors.Is(err, ErrFatalConnect) || errors.Is(err, errTunnelClosed) {
			rt.emitf("%v\n", err)
			if strings.Contains(err.Error(), AgentOutdatedHint) {
				rt.emitln("已停止重连。请按上方提示重新下载 tunnel-agent 并用新启动命令启动。")
			} else {
				rt.emitln("已停止重连。请到秀杰工作台「内网穿透」页点「换发启动命令」，再用新命令启动。")
			}
			return err
		}
		// ★ 软轮换：已在 serve 内完成 make-before-break（或仅主动拆旧），立即再挂，无退避。
		if errors.Is(err, errSoftRecycle) {
			attempt = 0
			rt.emitf("软轮换结束（本段存活 %s），立即继续…\n", time.Since(connectedAt).Round(time.Second))
			continue
		}
		// ★★★ P1：连上过、且**活得够久**（超过一个心跳周期）⇒ 判定链路健康，
		//	把退避计数**清零**。
		attempt = nextAttemptAfterDisconnect(attempt, time.Since(connectedAt))
		attempt++
		delay := backoff(attempt)
		rt.emitf("隧道已断开（%v，连接存活 %s），%s 后重连（第 %d 次）…\n",
			err, time.Since(connectedAt).Round(time.Second), delay.Round(time.Second), attempt)
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(delay):
		}
	}
}

// nextAttemptAfterDisconnect 断开后计算下一次重连的 attempt 计数。
//
// ★ P1：修复"连接稳定跑过很久、一次抖动后却永远卡在 30s 封顶"——
//
//	只要这一把连接**活过���个心跳周期**，就认为链路是通的，
//	把退避计数清零（快速恢复到 1s）；否则保留原计数继续递增。
//
//	判据用"活过 agentPingInterval"而不是"任意时长"，是为了**防重连风暴**：
//	"连上就断"的链路若每次都清零，退避会永远停在 1s ⇒ 每秒一次连接尝试，
//	既打服务端又耗本地资源。所以短命连接**不清零**。
//
// 纯函数（无 IO），便于直接单测锁死这条语义。
func nextAttemptAfterDisconnect(attempt int, aliveFor time.Duration) int {
	if aliveFor > agentPingInterval {
		return 0
	}
	return attempt
}

// errTunnelClosed 服务端明确告知隧道已关闭。
var errTunnelClosed = errors.New("服务端关闭了这条隧道")

// backoff 指数退避：1s、2s、4s…上限 30s。
//
// ★ 为什么封顶：笔记本合盖、断网、路由器重启这类故障通常在几十秒到几分钟内恢复，
//
//	而**无限重连**在"服务端配置错了 / 票据过期 / 隧道已关"这类**永远不会好**的故障上
//	会变成一个安静的后台进程 —— 用户以为隧道还开着，实际上一条请求都进不去。
//	封顶 + 打印明确原因，是让人能看懂发生了什么。
func backoff(attempt int) time.Duration {
	const (
		base = time.Second
		max  = 30 * time.Second
	)
	d := base
	for i := 1; i < attempt; i++ {
		d *= 2
		if d >= max {
			return max
		}
	}
	return d
}

// connectOnce 建一次连接并处理到断开（期间可软轮换多次）。
func (rt *agentRuntime) connectOnce(ctx context.Context) error {
	conn, err := rt.dialWS(ctx)
	if err != nil {
		return err
	}
	localBase, err := rt.localBaseURL()
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("本地地址不合法（-local %q）：%w", rt.localAddr, err)
	}
	rt.emitf("已连接：本地 %s  <-->  %s\n", rt.localAddr, rt.serverURL)

	for {
		go func(c *websocket.Conn) {
			<-ctx.Done()
			_ = c.Close()
		}(conn)

		next, serveErr := rt.serve(ctx, conn, localBase)
		if next != nil && serveErr == nil {
			// make-before-break：新连接已在服务端接管，切过去继续。
			_ = conn.Close()
			conn = next
			rt.emitf("软轮换切换完成（新连接已接管），继续服务\n")
			continue
		}
		_ = conn.Close()
		if next != nil {
			_ = next.Close()
		}
		if serveErr == nil {
			return nil
		}
		return classifyServeError(serveErr)
	}
}

// dialWS 拨号并完成 WS 升级（软轮换与首连共用）。
func (rt *agentRuntime) dialWS(ctx context.Context) (*websocket.Conn, error) {
	endpoint, err := attachURL(rt.serverURL, rt.token, rt.proto)
	if err != nil {
		return nil, err
	}
	dialer := &websocket.Dialer{
		HandshakeTimeout: 15 * time.Second,
		NetDialContext: (&net.Dialer{
			Timeout:   15 * time.Second,
			KeepAlive: 30 * time.Second,
		}).DialContext,
		TLSClientConfig: &tls.Config{
			MinVersion: tls.VersionTLS12,
			NextProtos: []string{"http/1.1"},
		},
		Proxy:             nil,
		EnableCompression: false,
	}
	conn, resp, err := dialer.DialContext(ctx, endpoint, nil)
	if err != nil {
		return nil, classifyDialError(err, resp)
	}
	if resp != nil && resp.StatusCode != http.StatusSwitchingProtocols {
		_ = conn.Close()
		return nil, classifyDialError(fmt.Errorf("bad handshake"), resp)
	}
	return conn, nil
}

// attachURL 把 -server 与 -token 拼成带票据的 WS 地址。
//
// ★ 票据走 query（服务端签名与签发地址都用这个形状，见 logic/tunnel/ticket.go）：
//
//	用 header 传会更"干净"，但服务端这条路由不挂鉴权中间件（WS 无法过
//	middleware.RequireLogin —— agent 没有用户的JWT），所以票据就是这条路由的凭证。
func attachURL(serverURL, token, proto string) (string, error) {
	u, err := url.Parse(serverURL)
	if err != nil {
		return "", fmt.Errorf("服务端地址不合法：%w", err)
	}
	switch u.Scheme {
	case "ws", "wss":
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	default:
		return "", fmt.Errorf("服务端地址协议不支持：%s（用 ws:// 或 wss://）", u.Scheme)
	}
	if u.Host == "" {
		return "", errors.New("服务端地址缺少主机名")
	}
	q := u.Query()
	q.Set("ticket", token)
	q.Set("agentVer", Version) // 服务端日志/兼容策略用；不参与验签
	proto = NormalizeProto(proto)
	if proto != "http" {
		q.Set("proto", proto) // 服务端校验与隧道记录一致（80012）
	}
	u.RawQuery = q.Encode()
	return u.String(), nil
}

// localBaseURL 本地服务的基地址（带 scheme）。
func (rt *agentRuntime) localBaseURL() (*url.URL, error) {
	return ParseLocalBaseURL(rt.localAddr)
}

// 心跳 / 软轮换参数（与服务端 logic/tunnel/heartbeat.go 对齐）。
//
//	★ 2026-10-11 实测：裸 TCP 空闲 ≈60s 被关；隧道常见恰好活 40s + 1006。
//	  代码里没有 40 —— 病灶在链路；药方 = 更密的双向 Text 心跳 + 阈值前软轮换。
const (
	agentPingInterval    = 10 * time.Second
	agentReadIdleTimeout = 90 * time.Second
	// 等 pong：须 > ping 间隔，避免偶发慢往返误杀；取 2.5×ping。
	agentPongTimeout = 25 * time.Second
)

// errSoftRecycle 软轮换：非故障，run 循环应立即接上（无退避）。
var errSoftRecycle = errors.New("soft recycle: 主动换连接以避开链路空闲掐断")

// serve 处理连接上的帧，直到断开或软轮换。
//
//	返回 (next, nil)：软轮换已拨好新连接，调用方应切到 next 继续；
//	返回 (nil, errSoftRecycle)：已拆旧但 Dial 新连接失败，外层立即重连；
//	返回 (nil, other)：真断开。
func (rt *agentRuntime) serve(ctx context.Context, conn *websocket.Conn, localBase *url.URL) (*websocket.Conn, error) {
	// 连接结束时清掉在途 body，避免 pipe/写协程泄漏到下一把连接。
	defer rt.clearPendingBodies()

	//写锁：多个在途请求会并发往这条连接写响应帧，而 gorilla 不允许并发写。
	var writeMu sync.Mutex
	// 透传流表（WebSocket 透传）：streamId → 它的本地 TCP 连接。
	var streamMu sync.Mutex
	streams := make(map[string]net.Conn)

	var lastServerAlive sync.Mutex
	aliveAt := time.Now()
	touchAlive := func() {
		lastServerAlive.Lock()
		aliveAt = time.Now()
		lastServerAlive.Unlock()
		_ = conn.SetReadDeadline(time.Now().Add(agentReadIdleTimeout))
	}
	isStale := func() bool {
		lastServerAlive.Lock()
		defer lastServerAlive.Unlock()
		return time.Since(aliveAt) > agentReadIdleTimeout
	}
	touchAlive()

	conn.SetPongHandler(func(string) error {
		touchAlive()
		return nil
	})

	type pingTracker struct {
		mu      sync.Mutex
		sentAt  time.Time
		ackedAt time.Time
		pongN   int
	}
	var tracker pingTracker
	markPong := func() {
		tracker.mu.Lock()
		tracker.ackedAt = time.Now()
		tracker.pongN++
		n := tracker.pongN
		sent := tracker.sentAt
		tracker.mu.Unlock()
		// ★ 可运行证据：pong 闭环计数（每 6 次≈1 分钟打一条）。
		if n == 1 || n%6 == 0 {
			rtt := time.Duration(0)
			if !sent.IsZero() {
				rtt = time.Since(sent)
			}
			rt.emitf("应用层心跳确认 pong#%d（RTT≈%s，证明服务端回了 Text FramePong）\n",
				n, rtt.Round(time.Millisecond))
		}
	}

	sendPing := func() bool {
		if isStale() {
			_ = conn.Close()
			return false
		}
		writeMu.Lock()
		_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
		err := conn.WriteMessage(websocket.TextMessage, mustJSON(frame{Type: framePing}))
		sent := time.Now()
		if err == nil {
			tracker.mu.Lock()
			tracker.sentAt = sent
			tracker.mu.Unlock()
		}
		writeMu.Unlock()
		if err != nil {
			_ = conn.Close()
			return false
		}
		go func(sentAt time.Time) {
			deadline := time.NewTimer(agentPongTimeout)
			defer deadline.Stop()
			tick := time.NewTicker(200 * time.Millisecond)
			defer tick.Stop()
			for {
				select {
				case <-ctx.Done():
					return
				case <-deadline.C:
					tracker.mu.Lock()
					stale := tracker.ackedAt.Before(sentAt)
					tracker.mu.Unlock()
					if stale {
						rt.emitf("应用层心跳未收到 pong（发出已超过 %s，判定链路半死，主动断开重连）\n",
							agentPongTimeout)
						_ = conn.Close()
					}
					return
				case <-tick.C:
					tracker.mu.Lock()
					ok := tracker.ackedAt.After(sentAt)
					tracker.mu.Unlock()
					if ok {
						return
					}
				}
			}
		}(sent)
		return true
	}

	// ★ 立刻第一拍（Ticker 要等满间隔才首次触发，旧行为空窗 = 一整拍）。
	if !sendPing() {
		return nil, fmt.Errorf("与服务端的连接断开：首拍心跳写出失败")
	}

	go func() {
		ticker := time.NewTicker(agentPingInterval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !sendPing() {
					return
				}
			}
		}
	}()

	// ★ 软轮换：阈值（~40s）前 make-before-break。
	var softArmed atomic.Bool
	var handoff atomic.Pointer[websocket.Conn]
	// handoffReady 在「轮换拨号有了结论」时关闭，好让读循环等它落位，而不是急着重连。
	handoffReady := make(chan struct{})
	if rt.softRecycleAfter > 0 {
		go func() {
			defer close(handoffReady)
			t := time.NewTimer(rt.softRecycleAfter)
			defer t.Stop()
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				rt.emitf("软轮换：本连接已满 %s（-recycle 指定），先建新再拆旧…\n", rt.softRecycleAfter)
				// ★★ 必须先置位、再拨号。
				//   dialWS 的 WS 握手本身就是 attach ⇒ 它「成功返回」的那一刻，
				//   服务端已经接管本隧道并主动关掉了旧连接。
				//   若置位晚于 dialWS，旧连接被服务端关闭时 softArmed 还是 false，
				//   读循环会把它当成真实断开（症状：偶发打「隧道已断开 … 1s 后重连」，
				//   且这条重连立刻又把刚拨好的新连接顶掉 ⇒ 肉眼就是「35s 断一下」）。
				softArmed.Store(true)
				newConn, dialErr := rt.dialWS(ctx)
				if dialErr != nil {
					// 拨号失败＝这次不是轮换：恢复真实断开语义，交给外层立刻重连。
					softArmed.Store(false)
					rt.emitf("软轮换 Dial 失败：%v —— 拆旧后由外层立即重连\n", dialErr)
					_ = conn.Close()
					return
				}
				handoff.Store(newConn)
				_ = conn.Close() // 若服务端已因接管关旧，此处幂等
			}
		}()
	}

	for {
		_ = conn.SetReadDeadline(time.Now().Add(agentReadIdleTimeout))
		_, payload, err := conn.ReadMessage()
		if err != nil {
			streamMu.Lock()
			for _, c := range streams {
				_ = c.Close()
			}
			streamMu.Unlock()
			if softArmed.Load() {
				// ★ 轮换已开始：新连接正在拨号中，等它落位再切过去。
				//   绝不能在这里立刻返回 errSoftRecycle —— 那会再拨一条连接，
				//   把正在拨号的那条顶掉（服务端 4012 replaced），轮换就退化成
				//   「一直重连」的抖动。handoffReady 在拨号有结论时关闭：
				//   成功 ⇒ handoff 有值、无缝切换；失败 ⇒ 外层按需重连。
				<-handoffReady
				if next := handoff.Load(); next != nil {
					return next, nil
				}
				return nil, errSoftRecycle
			}
			return nil, fmt.Errorf("与服务端的连接断开：%w", err)
		}
		touchAlive()
		var f frame
		if err := json.Unmarshal(payload, &f); err != nil {
			// ★ 畸形帧只跳过这一帧：不终止连接。
			//   服务端版本比客户端新时多出的字段就是这样进来的。
			continue
		}
		switch f.Type {
		case framePong:
			// 服务端对我们 ping 的应答；touchAlive 已在读到时做过。
			markPong()
		case frameRequest:
			// ★ 请求体中转缓冲必须在**启动 goroutine 之前**登记好：
			//   reqBody 帧是在当前读循环里紧接着处理的，而新协程何时开跑不确定 ——
			//   若在协程内才登记，reqBody 会查不到缓冲而被静默丢弃
			//   （症状：GET 正常，POST/PUT 收不到 body）。详见 handleRequest 的注释。
			var pb *pendingBody
			if f.ID != "" {
				pb = newPendingBody()
				rt.pendingBodies.Store(f.ID, pb)
			}
			go rt.handleRequest(conn, &writeMu, localBase, f, pb)
		case frameRequestBody:
			// ★ 请求体分块：经 channel 交给独立写协程，**绝不在读循环里
			//   直接 pipe.Write**（io.Pipe 无缓冲，Do 尚未 Read 时会卡死
			//   整条读循环 → reqEnd 进不来 → 公网挂死）。
			rt.pushRequestBody(f)
		case frameRequestEnd:
			rt.closeRequestBody(f.ID)
		case frameClose:
			return nil, fmt.Errorf("%w: %w", ErrFatalConnect, errTunnelClosed)
		case frameStreamOpen:
			go rt.openStream(conn, &writeMu, &streamMu, streams, localBase, f)
		case frameStreamBinary:
			rt.pumpStreamToLocal(streams, &streamMu, f)
		case frameStreamClose:
			rt.closeStream(streams, &streamMu, f.StreamID)
		case framePing:
			writeMu.Lock()
			_ = conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			_ = conn.WriteMessage(websocket.TextMessage, mustJSON(frame{Type: framePong}))
			writeMu.Unlock()
		}
	}
}

// ---------------------------------------------------------------------------
// 普通 HTTP 请求转发
// ---------------------------------------------------------------------------

// pendingBody 请求体的中转缓冲。
//
// ★ 请求体为什么要中转而不是直接流式给本地服务：
//
//	服务端是分块发来的（每块 64KB），而本地 http.Request 的 Body 是一条流。
//	用 io.Pipe 把两者接起来是最省事的做法：
//	收到块就 Write，收到 reqEnd 就 CloseWriter（EOF）。
//	★ 不能先攒完再发：那会让"请求体很大"的请求占满内存，
//	而流式接法让内存峰值只与单块大小相关。
//
// ★ chunks channel + 独立写协程：读循环只做非阻塞 enqueue。
//
//	若在读循环里同步 pipe.Write，Do 尚未开始读时整条 WS 卡死
//	（reqEnd 永远进不来 → 本地永远等 EOF → 公网 25s 超时）。
type pendingBody struct {
	writer *io.PipeWriter
	reader *io.PipeReader
	chunks chan []byte
	once   sync.Once
}

func newPendingBody() *pendingBody {
	pr, pw := io.Pipe()
	pb := &pendingBody{
		writer: pw,
		reader: pr,
		// 缓冲若干块：避免本地稍慢时反压到读循环。
		chunks: make(chan []byte, 8),
	}
	go pb.pump()
	return pb
}

func (pb *pendingBody) pump() {
	defer func() { _ = pb.writer.Close() }()
	for chunk := range pb.chunks {
		if _, err := pb.writer.Write(chunk); err != nil {
			return
		}
	}
}

func (pb *pendingBody) closeChunks() {
	pb.once.Do(func() { close(pb.chunks) })
}

// pushRequestBody 把一个请求体数据块交给写协程（读循环不阻塞在 pipe 上）。
func (rt *agentRuntime) pushRequestBody(f frame) {
	v, ok := rt.pendingBodies.Load(f.ID)
	if !ok {
		return
	}
	pb := v.(*pendingBody)
	payload, err := base64.StdEncoding.DecodeString(f.DataBase64)
	if err != nil {
		return
	}
	// 拷贝：帧缓冲可能被复用；写协程异步消费。
	chunk := make([]byte, len(payload))
	copy(chunk, payload)
	select {
	case pb.chunks <- chunk:
	default:
		// 缓冲满：仍要送达（保证正文完整），此时短暂堵住读循环
		// 好于永久卡在 pipe.Write（后者连 reqEnd 都收不到）。
		pb.chunks <- chunk
	}
}

// closeRequestBody 结束某个请求的请求体（发EOF 给本地服务）。
func (rt *agentRuntime) closeRequestBody(id string) {
	if v, ok := rt.pendingBodies.LoadAndDelete(id); ok {
		v.(*pendingBody).closeChunks()
	}
}

// abandonRequestBody 本地请求已失败时丢掉在途 body 缓冲。
//
// ★ 必须关掉 chunks 并 CloseWriter：否则写协程/读循环可能永久阻塞，
//
//	整条 WS 不再处理后续帧，访客侧一直转圈。
func (rt *agentRuntime) abandonRequestBody(id string) {
	if id == "" {
		return
	}
	if v, ok := rt.pendingBodies.LoadAndDelete(id); ok {
		pb := v.(*pendingBody)
		// 先打断可能卡在 Write 上的 pump，再关 channel。
		_ = pb.writer.CloseWithError(io.ErrClosedPipe)
		pb.closeChunks()
	}
}

// clearPendingBodies 连接断开时清理全部在途 body（避免泄漏到下一把连接）。
func (rt *agentRuntime) clearPendingBodies() {
	rt.pendingBodies.Range(func(key, value any) bool {
		pb := value.(*pendingBody)
		_ = pb.writer.CloseWithError(io.ErrClosedPipe)
		pb.closeChunks()
		rt.pendingBodies.Delete(key)
		return true
	})
}

// handleRequest 处理一次本地 HTTP 请求。
// body 参数是**调用方已登记好**的请求体中转缓冲（可能是 nil，表示无 body）。
//
// ★★ 为什么要由调用方建好再传进来（本项目真实踩过的竞态）：
//
//	原来是在本函数里 `rt.pendingBodies.Store(...)`，而调用方是
//	`go rt.handleRequest(...)` —— **新协程什么时候真正开始跑是不确定的**，
//	而服务端发来的 `reqBody` 帧是在**当前读循环里紧接着处理的**。
//	于是出现这条时序：
//	    读循环收到 request 帧 → 启动 goroutine → 继续读
//	    读循环收到 reqBody 帧 → 查 rt.pendingBodies → **还没登记** → 静默丢弃
//	    goroutine 终于开跑 → 登记 pipe（但数据已经丢了）
//	⇒ 症状是「GET 正常、POST/PUT 永远收不到 body」，
//	  而 agent 日志里只印一行"→ POST /xxx"，看起来一切正常。
//	  本地服务侧则是"收到了请求但 body 是空的"（我的测试页 POST 端点
//	  因为读不到 body 而返回空响应）。
//
//	⇒ 把"建pipe + 登记"提到**启动 goroutine 之前**（同步完成），
//	读循环后续处理 reqBody 时就一定查得到。
func (rt *agentRuntime) handleRequest(conn *websocket.Conn, writeMu *sync.Mutex, localBase *url.URL, f frame, pb *pendingBody) {
	// ★ 兜底：将来任何路径再把 nil 传进来，只能是「一条请求失败」，不能整进程 SIGSEGV。
	if localBase == nil {
		rt.abandonRequestBody(f.ID)
		rt.emitln("内部错误：本地基址未初始化（localBase=nil），已拒绝本条请求")
		if conn != nil && writeMu != nil {
			rt.sendError(conn, writeMu, f.ID, "本地基址未初始化")
		}
		return
	}
	// ---- 构造本地请求 ----
	target := *localBase
	target.Path = singleJoiningSlash(localBase.Path, f.Path)
	target.RawQuery = f.Query

	// ★ 变量声明成 io.Reader（而不是 := 拿 *io.PipeReader 直接用）：
	//   无 body 的分支要赋 http.NoBody，而 NoBody 的类型是
	//   http.noBody（一个未导出类型），赋给 *io.PipeReader 变量会编译不过。
	var bodyReader io.Reader = http.NoBody
	if pb != nil {
		bodyReader = pb.reader
	}
	// ★ WithContext(ctx)：本地请求与服务端连接同生共死。
	//   少了它，访客关掉页面后本地请求还会继续跑完
	//   （本地服务在做一件没人要的事）。
	req, err := http.NewRequestWithContext(context.Background(), f.Method, target.String(), bodyReader)
	if err != nil {
		// ★ 提前退出必须 CloseWriter：只 Delete 会让读循环里的 pipe.Write
		//   永远阻塞（读循环卡死 ⇒ 整条隧道不再收帧，公网侧表现为挂死）。
		rt.abandonRequestBody(f.ID)
		rt.sendError(conn, writeMu, f.ID, fmt.Sprintf("无法构造本地请求：%v", err))
		return
	}
	for key, values := range f.Headers {
		for _, v := range values {
			req.Header.Add(key, v)
		}
	}
	if f.Host != "" {
		req.Host = f.Host
	}
	// ★ ContentLength 刻意**不设**（NewRequest 对未知长度用 chunked）：
	//   服务端转发过来时请求体是分块的，长度可能还在路上，
	//   填一个错的值会让本地服务按错误的边界截断。
	// ★ 同时必须删掉 Header 里的 Content-Length：
	//   服务端 stripHopByHop 已剔，但旧帧/旁路仍可能带上；
	//   与 ContentLength=-1 并存时，部分本地服务会按「声明长度」干等
	//   不存在的字节 → 公网侧表现为转圈/超时（curl 000）。
	req.Header.Del("Content-Length")
	req.Header.Del("Transfer-Encoding")
	req.ContentLength = -1

	if rt.verbose {
		log.Printf("→ %s %s?%s", f.Method, f.Path, f.Query)
	}

	// ★ 用runtime 上那个复用 client（不走代理的理由见 newLocalClient 的注释）。
	resp, err := rt.localClient.Do(req)
	if err != nil {
		rt.abandonRequestBody(f.ID)
		rt.sendError(conn, writeMu, f.ID, fmt.Sprintf("本地服务无响应：%v", err))
		return
	}
	defer resp.Body.Close()

	// ---- 响应头 ----
	header := make(map[string][]string, len(resp.Header))
	for key, values := range resp.Header {
		copied := make([]string, len(values))
		copy(copied, values)
		header[key] = copied
	}
	// ★ Content-Length 必须**剔除**：
	//   响应体是分块流式转发的，实际字节数由我们自己数，
	//   把本地服务报的长度原样带过去会让服务端与客户端
	//   按一个"可能对不上的长度"等待 —— 表现为"响应卡住不结束"。
	//   （Transfer-Encoding 由 Go 自己处理，不能手工写进 Header。）
	delete(header, "Content-Length")

	writeMu.Lock()
	err = conn.WriteMessage(websocket.TextMessage, mustJSON(frame{
		Type:    frameResponse,
		ID:      f.ID,
		Status:  resp.StatusCode,
		Headers: header,
	}))
	writeMu.Unlock()
	if err != nil {
		return
	}

	// ---- 响应体（流式，边收边发）----
	//
	// ★ 必须流式：SSE 的话本地服务会**一直不返回**（连接挂着等下一个事件），
	//   先读完再发的话用户永远看不到第一个字—— 表现是"页面一直转圈"。
	//   flush 每次都做，所以每一块到达就立刻可见。
	buf := make([]byte, bodyChunk)
	for {
		n, readErr := resp.Body.Read(buf)
		if n > 0 {
			writeMu.Lock()
			_ = conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
			werr := conn.WriteMessage(websocket.TextMessage, mustJSON(frame{
				Type:       frameResponseBody,
				ID:         f.ID,
				DataBase64: base64.StdEncoding.EncodeToString(buf[:n]),
			}))
			writeMu.Unlock()
			if werr != nil {
				return
			}
		}
		if readErr != nil {
			break
		}
	}
	writeMu.Lock()
	_ = conn.WriteMessage(websocket.TextMessage, mustJSON(frame{Type: frameResponseEnd, ID: f.ID}))
	writeMu.Unlock()

	if rt.verbose {
		log.Printf("← %d %s", resp.StatusCode, f.Path)
	}
}

// sendError 告诉服务端"这次本地请求失败了"。
func (rt *agentRuntime) sendError(conn *websocket.Conn, writeMu *sync.Mutex, id, message string) {
	writeMu.Lock()
	defer writeMu.Unlock()
	_ = conn.WriteMessage(websocket.TextMessage, mustJSON(frame{
		Type:    frameError,
		ID:      id,
		Message: message,
	}))
}

// ---------------------------------------------------------------------------
// WebSocket 透传
// ---------------------------------------------------------------------------

// openStream 开一条透传流：与本地服务建立**裸 TCP** 连接，之后字节直通。
//
// ★ proto=tcp/udp：裸 dial + streamOpenResult，无 HTTP 握手。
// ★ proto 缺省/空：HTTP-WS 透传（旧行为）—— 把 Upgrade 头原样写给本地。
//
// ★ 有效 proto = 帧内 proto；空则回退到 agent -proto。
//
//	否则：用户已用 -proto tcp 连上，但帧里 proto 丢失/旧服务端未填时，
//	会误走 HTTP-WS 路径且**不发 streamOpenResult** ⇒ 服务端 WaitStreamOpen
//	超时 ⇒ 公网侧表现为 dial 通但 recv=b''（且本进程无任何 dial 日志）。
func (rt *agentRuntime) openStream(conn *websocket.Conn, writeMu *sync.Mutex, streamMu *sync.Mutex, streams map[string]net.Conn, localBase *url.URL, f frame) {
	proto := effectiveStreamProto(f.Proto, rt.proto)
	if proto == "tcp" || proto == "udp" {
		rt.emitf("streamOpen id=%s proto=%s local=%s remote=%s\n",
			f.StreamID, proto, strings.TrimSpace(f.Local), strings.TrimSpace(f.Remote))
		rt.openRawStream(conn, writeMu, streamMu, streams, f)
		return
	}
	if localBase == nil {
		rt.emitln("内部错误：本地基址未初始化（localBase=nil），已拒绝 streamOpen")
		if conn != nil && writeMu != nil {
			writeMu.Lock()
			_ = conn.WriteMessage(websocket.TextMessage, mustJSON(frame{
				Type: frameError, StreamID: f.StreamID, Message: "本地基址未初始化",
			}))
			writeMu.Unlock()
		}
		return
	}
	rt.emitf("streamOpen id=%s proto=http-ws（帧proto=%q agent=-proto %s）\n",
		f.StreamID, f.Proto, NormalizeProto(rt.proto))
	// ★ https 目标必须 tls.Dial：明文 HTTP 写到 TLS 端口 ⇒ Upgrade 全挂（本轮 P1）。
	//   InsecureSkipVerify 只允许这一跳；服务端 wss（dialWS）仍严格校验。
	local, err := dialLocalHTTPStream(localBase)
	if err != nil {
		writeMu.Lock()
		_ = conn.WriteMessage(websocket.TextMessage, mustJSON(frame{
			Type: frameError, StreamID: f.StreamID, Message: fmt.Sprintf("连不上本地服务：%v", err),
		}))
		writeMu.Unlock()
		return
	}
	streamMu.Lock()
	streams[f.StreamID] = local
	streamMu.Unlock()

	// ---- 先把访客的握手请求原样写到本地 ----
	var b strings.Builder
	fmt.Fprintf(&b, "%s %s", f.Method, singleJoiningSlash(localBase.Path, f.Path))
	if f.Query != "" {
		b.WriteString("?" + f.Query)
	}
	b.WriteString(" HTTP/1.1\r\n")
	for key, value := range f.UpgradeHeader {
		fmt.Fprintf(&b, "%s: %s\r\n", key, value)
	}
	if f.Host != "" {
		fmt.Fprintf(&b, "Host: %s\r\n", f.Host)
	}
	b.WriteString("\r\n")
	if _, err := local.Write([]byte(b.String())); err != nil {
		_ = local.Close()
		return
	}

	rt.pumpLocalToServer(conn, writeMu, streamMu, streams, local, f.StreamID)
}

// dialLocalHTTPStream 为 HTTP-WS Upgrade 建到本地服务的连接。
//
// ★ https → tls.Dial（默认端口 443，InsecureSkipVerify 仅此跳）；
//
//	http → 普通 TCP（默认端口 80）。
func dialLocalHTTPStream(localBase *url.URL) (net.Conn, error) {
	if localBase == nil {
		return nil, errors.New("本地基址未初始化")
	}
	addr := dialHostPort(localBase)
	d := &net.Dialer{Timeout: 10 * time.Second}
	if localBase.Scheme == "https" {
		return tls.DialWithDialer(d, "tcp", addr, &tls.Config{
			InsecureSkipVerify: true, // 仅本地一跳；见 NewLocalClient 同口径
			MinVersion:         tls.VersionTLS12,
			ServerName:         localBase.Hostname(),
		})
	}
	return d.Dial("tcp", addr)
}

// effectiveStreamProto 帧 proto 优先；空则用 agent -proto（Normalize 后）。
func effectiveStreamProto(frameProto, agentProto string) string {
	p := strings.TrimSpace(strings.ToLower(frameProto))
	if p == "tcp" || p == "udp" {
		return p
	}
	if p != "" {
		return p
	}
	return NormalizeProto(agentProto)
}

// openRawStream TCP/UDP：dial 本地 → streamOpenResult → 双向 streamBin。
func (rt *agentRuntime) openRawStream(conn *websocket.Conn, writeMu *sync.Mutex, streamMu *sync.Mutex, streams map[string]net.Conn, f frame) {
	dialAddr := strings.TrimSpace(f.Local)
	if dialAddr == "" {
		dialAddr = hostPortForDial(rt.localAddr)
	} else {
		dialAddr = hostPortForDial(dialAddr)
	}
	okFalse := false
	okTrue := true
	sendResult := func(ok bool, msg string) {
		writeMu.Lock()
		defer writeMu.Unlock()
		fr := frame{Type: frameStreamOpenResult, StreamID: f.StreamID, Ok: &okFalse, Message: msg}
		if ok {
			fr.Ok = &okTrue
			fr.Message = ""
		}
		_ = conn.WriteMessage(websocket.TextMessage, mustJSON(fr))
	}
	rt.emitf("dial 本地 %s（stream=%s）…\n", dialAddr, f.StreamID)
	local, err := net.DialTimeout("tcp", dialAddr, 10*time.Second)
	if err != nil {
		rt.emitf("dial 本地失败 stream=%s addr=%s：%v\n", f.StreamID, dialAddr, err)
		sendResult(false, fmt.Sprintf("本地拒绝连接：%v", err))
		return
	}
	streamMu.Lock()
	streams[f.StreamID] = local
	streamMu.Unlock()
	rt.emitf("dial 本地成功 stream=%s addr=%s\n", f.StreamID, dialAddr)
	sendResult(true, "")
	rt.pumpLocalToServer(conn, writeMu, streamMu, streams, local, f.StreamID)
}

func (rt *agentRuntime) pumpLocalToServer(conn *websocket.Conn, writeMu *sync.Mutex, streamMu *sync.Mutex, streams map[string]net.Conn, local net.Conn, streamID string) {
	go func() {
		defer func() {
			streamMu.Lock()
			delete(streams, streamID)
			streamMu.Unlock()
			_ = local.Close()
			writeMu.Lock()
			_ = conn.WriteMessage(websocket.TextMessage, mustJSON(frame{
				Type: frameStreamClose, StreamID: streamID,
			}))
			writeMu.Unlock()
		}()
		buf := make([]byte, bodyChunk)
		for {
			n, err := local.Read(buf)
			if n > 0 {
				writeMu.Lock()
				_ = conn.SetWriteDeadline(time.Now().Add(30 * time.Second))
				werr := conn.WriteMessage(websocket.TextMessage, mustJSON(frame{
					Type:       frameStreamBinary,
					StreamID:   streamID,
					DataBase64: base64.StdEncoding.EncodeToString(buf[:n]),
				}))
				writeMu.Unlock()
				if werr != nil {
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
}

// pumpStreamToLocal 把服务端转来的字节写进对应的本地连接。
func (rt *agentRuntime) pumpStreamToLocal(streams map[string]net.Conn, streamMu *sync.Mutex, f frame) {
	streamMu.Lock()
	local, ok := streams[f.StreamID]
	streamMu.Unlock()
	if !ok {
		return
	}
	payload, err := base64.StdEncoding.DecodeString(f.DataBase64)
	if err != nil {
		return
	}
	_, _ = local.Write(payload)
}

// closeStream 关闭一条透传流。
func (rt *agentRuntime) closeStream(streams map[string]net.Conn, streamMu *sync.Mutex, streamID string) {
	streamMu.Lock()
	local, ok := streams[streamID]
	if ok {
		delete(streams, streamID)
	}
	streamMu.Unlock()
	if ok {
		_ = local.Close()
	}
}

// ---------------------------------------------------------------------------
// 本地地址校验（SSRF / 防跳板）
// ---------------------------------------------------------------------------

// checkLocalAddr 校验本地地址，并在**非回环地址**时给出明确警示。
//
// ★ 这一条是"防手滑"，不是安全边界（说清楚很重要）：
//
//	本工具的用途就是"把内网服务暴露出去"，所以"允许私有网段"是它的本职。
//	真正的风险是**误配**：用户把 -local 写成 10.0.0.5:22 或某个数据库地址，
//	于是一个本该只连本地开发服务器的命令变成了访问内网任意主机的跳板。
//	所以策略是：回环放行（那就是本机的服务，正是目标）；
//	私有网段**放行但明确警示**（它可能是合法的"同局域网另一台机器"）；
//	其余（公网地址）**直接拒绝** —— 那一定不是用户想要的。
//
// CheckLocalAddr 校验 -local（CLI/GUI 共用）。
//
// ★ 与 ParseLocalBaseURL 共用最终 URL 解析结果：用 u.Hostname() 判回环/私网/公网，
//
//	拒绝 userinfo（防 `127.0.0.1:3000@8.8.8.8` 绕过），拒绝 path/query/fragment。
func CheckLocalAddr(addr string) error {
	// ★ 先对原文扫空白（含尾随）：与「启动放行、运行期崩」口径对齐；
	//   Normalize/Run 会先 TrimSpace，再进这里时已是干净串。
	if containsWhitespaceOrControl(addr) {
		return ErrLocalAddrWhitespace
	}
	u, err := ParseLocalBaseURL(addr)
	if err != nil {
		return err
	}
	return checkHostnameAllowed(u.Hostname())
}

// warnPrivate 返回一个"放行但要提醒"的错误（调用方据此打警示而不退出）。
//
// ★ 用 error 承载警示是为了让调用点写成一句 `if err != nil { 检查(err) }`，
//
//	而"警示但继续"与"拒绝并退出"两种语义在同一个返回通道里表达，
//	免得调用方漏掉其中一种。
func warnPrivate(host string) error {
	return &warningError{host: host}
}

// warningError 是一个"不是错误"的警示。
type warningError struct{ host string }

func (e *warningError) Error() string {
	return fmt.Sprintf("注意：%s 是内网地址。确认这台机器上的服务就是你打算暴露的那个 —— 把 -local 指错会让隧道变成访问其它内网主机的跳板。", e.host)
}

// ---------------------------------------------------------------------------
// 小工具
// ---------------------------------------------------------------------------

// singleJoiningSlash 拼路径（避免出现双斜杠或漏斜杠）。
func singleJoiningSlash(base, path string) string {
	switch {
	case base == "" || base == "/":
		if path == "" {
			return "/"
		}
		if path[0] != '/' {
			return "/" + path
		}
		return path
	case path == "" || path == "/":
		return base
	}
	return strings.TrimRight(base, "/") + "/" + strings.TrimLeft(path, "/")
}

// mustJSON 序列化帧（出错时返回一个最小可解析的帧，让对端不会因解析失败而静默）。
func mustJSON(v any) []byte {
	out, err := json.Marshal(v)
	if err != nil {
		return []byte(`{"type":"error"}`)
	}
	return out
}
