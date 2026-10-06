# tunnel-agent

内网穿透客户端 —— 把本机的一个 HTTP 服务暴露成公网可访问的地址，形态参照
[ngrok](https://ngrok.com/) / [localtunnel](https://theboroer.github.io/localtunnel/)。

**本仓库只包含客户端**（单文件 Go 程序，864 行）。服务端不在这里 ——
你需要一个签发票据、建立隧道分发的服务端才能用它（见[与本仓库无关的部分](#与本仓库无关的部分)）。

---

## 它解决的问题

你的服务跑在本机或内网（比如 `127.0.0.1:3000` 的 Vite 开发服务器），
别人访问不到。传统做法是开端口、做端口映射、申请公网 IP。

`tunnel-agent` 反过来：**只从你的机器向外拨一条 WebSocket 长连接**，
所有入站请求都由服务端在这条连接上反向下发。于是：

- 不需要公网 IP
- 不需要在路由器上做端口映射
- 本机**不需要**监听任何额外端口（你的服务本来就在监听，agent 只是转发）
- 关掉 agent，地址立即失效

---

## 安装

需要 Go 1.24 或更高版本。

```bash
go install github.com/xiujiecn/tunnel-agent@latest
```

或者从源码构建：

```bash
git clone https://github.com/xiujiecn/tunnel-agent.git
cd tunnel-agent
go build -o tunnel-agent .
```

### 直接下载

[Releases](https://github.com/xiujiecn/tunnel-agent/releases) 页面提供六个平台的预编译产物
（macOS / Linux / Windows × 各自常见架构），全部静态链接，下载后 `chmod +x` 即可运行，
并附 `SHA256SUMS.txt`。

也可以自己交叉编译：

```bash
CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build -o tunnel-agent-darwin-arm64 .
CGO_ENABLED=0 GOOS=windows GOARCH=amd64  go build -o tunnel-agent-windows-amd64.exe .
CGO_ENABLED=0 GOOS=linux   GOARCH=amd64  go build -o tunnel-agent-linux-amd64 .
```

### 校验产物架构

交叉编译最常见的错误是「环境变量没生效，于是六个文件其实是同一种架构」——
它在 CI 上看起来完全正常（都构建成功），只有下载后在错误的机器上跑才会炸。

```bash
bash scripts/verify-artifacts.sh   # 逐个核对 dist/ 里产物的真实架构
```

这条检查也在 CI 与发布流程里各跑一次。

---

## 用法

```bash
tunnel-agent \
  -server wss://<你的服务端>/api/v1/tunnel/agent/attach \
  -token  <绑定票据> \
  -local  127.0.0.1:3000 \
  -v
```

| 参数 | 说明 |
|---|---|
| `-server` | 服务端 agent 接入地址（`ws://` 或 `wss://`），必填 |
| `-token` | 绑定票据，必填（由服务端签发） |
| `-local` | 要暴露的本地服务地址，默认 `127.0.0.1:3000` |
| `-v` | 打印每条请求的转发明细 |

带上 `-v` 之后，每条经由隧道进来的请求都会打印一行，便于确认"到底有没有流量"：

```
→ GET /api/users?id=1
← 200 1.2KB 34ms
```

---

## ⚠️ 安全须知

**这个工具会把你的本地服务暴露到公网。任何拿到公网地址的人都能访问它。**

- 只把地址分享给确实需要访问的人
- 隧道地址是临时的：关掉 agent 就立即失效
- 票据是**一次性**的：用过即作废，agent 断线重连需要重新获取

`-local` 只允许回环地址（`127.0.0.1` / `::1` / `localhost`）或私有网段。
**它不是安全边界，而是一道防手滑的闸** —— 把 `-local` 写成某个内网 IP，
会让你的 agent 变成访问那台机器的跳板。

---

## 工作原理

```
公网访客 ──HTTP──> 服务端 ──WebSocket(已建立的连接)──> tunnel-agent ──HTTP──> 你的本地服务
                    ↑                                    │
                    └────────── 响应原路返回 ─────────────┘
```

连接由 **agent 主动发起**（出方向），所以本地不需要接受任何入站连接。
服务端在这条连接上以 JSON 帧下发请求，agent 转发给本地服务，再把响应帧送回去。

帧类型（`main.go` 里的常量）：

| 帧 | 方向 | 用途 |
|---|---|---|
| `request` | 服务端 → agent | 开始一次本地请求（带 method / path / query / headers） |
| `reqBody` / `reqEnd` | 服务端 → agent | 请求体数据块 / 结束 |
| `response` | agent → 服务端 | 响应状态与头部 |
| `resBody` / `resEnd` | agent → 服务端 | 响应体数据块 / 结束 |
| `error` | agent → 服务端 | 本地请求失败（连不上、超时等） |
| `close` | 服务端 → agent | 隧道已关闭，优雅退出 |
| `ping` / `pong` | 双向 | 保活 |
| `streamOpen` / `streamBin` / `streamClose` | 双向 | WebSocket 透传流 |

请求体与响应体都是**分块流式转发**的 —— 所以大文件不会一次性读进内存。

---

## 几个实现上的选择

这些是容易被忽略、但踩了会很痛的地方：

**① ALPN 锁 `http/1.1`**

WebSocket 走的是 RFC 6455 的 HTTP/1.1 Upgrade 握手。不限制 ALPN 时，
Go 会与 nginx 协商出 h2，而 nginx 1.24 在 h2 上走 RFC 8441 路径会**把 Upgrade 头剥掉**。
症状是客户端只报 `websocket: bad handshake`（拿不到状态码与响应体），
而服务端日志里连一条都没有 —— 看起来像"服务端拒了我"，实际是请求根本没到。

**② 握手不走环境代理**

`HTTP_PROXY` 这类环境变量对 `wss://` 会走 HTTP CONNECT 隧道，
而绝大多数企业代理要么直接拒绝、要么剥掉 Upgrade 头。
客户端握手与本地转发都**显式不走代理**（`Proxy: nil`）。

**③ 本地转发复用同一个 http.Client**

新建 client 意味着连接池归零，页面几十个静态资源就是几十次重新三次握手，
本地服务的 listen backlog 很容易被打满 —— 症状是"单请求能过，多资源就失败"。

**④ 请求体中转缓冲必须在起协程之前登记**

服务端发来的 `reqBody` 帧是在当前读循环里紧接着处理的，
而新协程何时开跑是不确定的。若在协程内部才登记缓冲，
`reqBody` 会查不到而被**静默丢弃** —— 症状是 GET 正常、POST/PUT 永远收不到 body，
且两端都不报错。

---

## 开发

```bash
go build ./...      # 构建
go test ./...       # 测试（4 条，含两条静态结构守卫）
go vet ./...        # 静态检查
gofmt -l .          # 格式检查，应无输出

bash scripts/verify-artifacts.sh   # 校验交叉编译产物的架构（先go build 出 dist/）
```

发布：`git tag v0.1.0 && git push --tags` —— tag 会触发
[release.yml](.github/workflows/release.yml) 构建六个平台并创建 Release。

测试里有两条**扫源码**的守卫（断言"登记发生在 `go` 语句之前"这类结构事实），
因为那类竞态在行为层面很难稳定复现，而"写法本身就是错的"可以直接断言。

---

## 与本仓库无关的部分

本仓库只有客户端。要真正跑起来，你需要服务端实现：

- **签发票据**：一次性、绑 IP、带用途与 TTL 的凭证
- **隧道注册与分发**：把子域映射到某条 agent 连接
- **入站代理**：HTTP 解析、按 Host 分发到对应 agent、计费/配额

协议本身很简单（见上面的帧表），一个最小实现大约几百行。

---

## License

MIT —— 见 [LICENSE](LICENSE)。