package gui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strings"
	"sync"

	"github.com/xiujiecn/tunnel-agent/agent"
	"github.com/xiujiecn/tunnel-agent/lib"
)

// ConsoleID 控制台伪日志的固定 ID（托盘/进程级消息，不与任何隧道混）。
const ConsoleID = "console"

// Tunnel 一条可独立启停的隧道（每条自持 context + LogBuffer）。
type Tunnel struct {
	ID        string
	ServerURL string
	Token     string
	LocalAddr string
	Proto     string
	Verbose   bool

	cancel  context.CancelFunc
	running bool
	lastErr string
	logs    *LogBuffer
}

// TunnelInfo 列表/API 回显（token 仅掩码）。
type TunnelInfo struct {
	ID      string `json:"id"`
	Server  string `json:"server"`
	Token   string `json:"token"` // 掩码，从不回显完整票据
	Local   string `json:"local"`
	Proto   string `json:"proto"`
	Running bool   `json:"running"`
	Error   string `json:"error,omitempty"`
}

// Controller 管理多条隧道（GUI / 托盘共用）。
type Controller struct {
	mu      sync.Mutex
	tunnels map[string]*Tunnel
	console *LogBuffer // 托盘/进程级汇总日志
}

func NewController() *Controller {
	return &Controller{
		tunnels: make(map[string]*Tunnel),
		console: NewLogBuffer(2000),
	}
}

// Logs 返回控制台伪日志（兼容旧调用名；托盘 Notify 写这里）。
func (c *Controller) Logs() *LogBuffer { return c.console }

// LogsOf 返回指定隧道的日志缓冲；id 为 ConsoleID 时返回控制台日志。
func (c *Controller) LogsOf(id string) *LogBuffer {
	if id == ConsoleID {
		return c.console
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	t := c.tunnels[id]
	if t == nil {
		return nil
	}
	return t.logs
}

// Running 是否至少有一条在跑（给旧 /api/status）。
func (c *Controller) Running() bool {
	return c.RunningCount() > 0
}

// RunningCount 当前 running 的隧道数。
func (c *Controller) RunningCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := 0
	for _, t := range c.tunnels {
		if t.running {
			n++
		}
	}
	return n
}

// TotalCount 已登记隧道数（不含控制台伪项）。
func (c *Controller) TotalCount() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.tunnels)
}

// LastError 任一条的最近错误（汇总用；优先取仍 running=false 且有 error 的）。
func (c *Controller) LastError() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, t := range c.tunnels {
		if t.lastErr != "" {
			return t.lastErr
		}
	}
	return ""
}

// List 返回所有隧道快照（token 已掩码）。
func (c *Controller) List() []TunnelInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	out := make([]TunnelInfo, 0, len(c.tunnels))
	for _, t := range c.tunnels {
		out = append(out, tunnelInfoLocked(t))
	}
	return out
}

func tunnelInfoLocked(t *Tunnel) TunnelInfo {
	return TunnelInfo{
		ID:      t.ID,
		Server:  t.ServerURL,
		Token:   maskToken(t.Token),
		Local:   t.LocalAddr,
		Proto:   t.Proto,
		Running: t.running,
		Error:   t.lastErr,
	}
}

// Get 单条快照；不存在返回 false。
func (c *Controller) Get(id string) (TunnelInfo, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := c.tunnels[id]
	if t == nil {
		return TunnelInfo{}, false
	}
	return tunnelInfoLocked(t), true
}

// Add 登记一条隧道；start 为 true 时立即启动。返回新 ID。
func (c *Controller) Add(cfg agent.Config, start bool) (string, error) {
	cfg.Proto = agent.NormalizeProto(cfg.Proto)
	if cfg.LocalAddr == "" {
		cfg.LocalAddr = "127.0.0.1:3000"
	}
	if cfg.ServerURL == "" {
		return "", fmt.Errorf("缺少 -server")
	}
	if cfg.Token == "" {
		return "", fmt.Errorf("缺少 -token")
	}

	id := newTunnelID()
	t := &Tunnel{
		ID:        id,
		ServerURL: cfg.ServerURL,
		Token:     cfg.Token,
		LocalAddr: cfg.LocalAddr,
		Proto:     cfg.Proto,
		Verbose:   cfg.Verbose,
		logs:      NewLogBuffer(2000),
	}

	c.mu.Lock()
	c.tunnels[id] = t
	c.mu.Unlock()

	c.console.Append(fmt.Sprintf("已添加隧道 %s：local=%s proto=%s", id, cfg.LocalAddr, cfg.Proto))
	if start {
		if err := c.Start(id); err != nil {
			return id, err
		}
	}
	return id, nil
}

// AddFromFields 从表单字段添加。
func (c *Controller) AddFromFields(server, token, local, proto string, verbose, start bool) (string, error) {
	p, err := lib.ParseFields(server, token, local, proto, verbose)
	if err != nil {
		return "", err
	}
	return c.Add(agent.Config{
		ServerURL: p.ServerURL,
		Token:     p.Token,
		LocalAddr: p.LocalAddr,
		Proto:     p.Proto,
		Verbose:   p.Verbose,
	}, start)
}

// AddFromPaste 从整行启动命令添加。
func (c *Controller) AddFromPaste(line string, start bool) (string, error) {
	p, err := lib.ParseCommandLine(line)
	if err != nil {
		return "", err
	}
	return c.Add(agent.Config{
		ServerURL: p.ServerURL,
		Token:     p.Token,
		LocalAddr: p.LocalAddr,
		Proto:     p.Proto,
		Verbose:   p.Verbose,
	}, start)
}

// Start 启动指定隧道（不影响其他条）。
func (c *Controller) Start(id string) error {
	c.mu.Lock()
	t := c.tunnels[id]
	if t == nil {
		c.mu.Unlock()
		return errTunnelNotFound
	}
	if t.running {
		c.mu.Unlock()
		return nil
	}
	cfg := agent.Config{
		ServerURL: t.ServerURL,
		Token:     t.Token,
		LocalAddr: t.LocalAddr,
		Proto:     t.Proto,
		Verbose:   t.Verbose,
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.cancel = cancel
	t.running = true
	t.lastErr = ""
	logs := t.logs
	c.mu.Unlock()

	w := lineWriter{buf: logs}
	logs.Append(fmt.Sprintf("启动隧道：local=%s proto=%s server=%s", cfg.LocalAddr, cfg.Proto, cfg.ServerURL))

	go func() {
		err := agent.Run(ctx, cfg, w)
		c.mu.Lock()
		cur := c.tunnels[id]
		if cur == nil {
			c.mu.Unlock()
			return
		}
		cur.running = false
		cur.cancel = nil
		if err != nil && ctx.Err() == nil {
			cur.lastErr = err.Error()
			cur.logs.Append("错误：" + err.Error())
		} else if ctx.Err() != nil {
			cur.logs.Append("已停止。")
		}
		c.mu.Unlock()
	}()
	return nil
}

// Stop 只停止指定隧道。
func (c *Controller) Stop(id string) error {
	c.mu.Lock()
	t := c.tunnels[id]
	if t == nil {
		c.mu.Unlock()
		return errTunnelNotFound
	}
	cancel := t.cancel
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	return nil
}

// StopAll 停止全部隧道（托盘「停止全部」）。
func (c *Controller) StopAll() {
	c.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(c.tunnels))
	for _, t := range c.tunnels {
		if t.cancel != nil {
			cancels = append(cancels, t.cancel)
		}
	}
	c.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
	c.console.Append("已请求停止全部隧道。")
}

// Remove 先 stop 再删表项。
func (c *Controller) Remove(id string) error {
	if err := c.Stop(id); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.tunnels[id]; !ok {
		return errTunnelNotFound
	}
	delete(c.tunnels, id)
	c.console.Append("已删除隧道 " + id)
	return nil
}

var errTunnelNotFound = fmt.Errorf("隧道不存在")

func newTunnelID() string {
	var b [4]byte
	if _, err := rand.Read(b[:]); err != nil {
		// 极罕见：退化为时间无关的固定前缀 + 再试一次
		return fmt.Sprintf("t%08x", len(b))
	}
	return hex.EncodeToString(b[:])
}

// maskToken 回显用：短票全掩；长票保留首尾各 4 位。
func maskToken(tok string) string {
	tok = strings.TrimSpace(tok)
	n := len(tok)
	if n == 0 {
		return ""
	}
	if n <= 8 {
		return "********"
	}
	return tok[:4] + "…" + tok[n-4:]
}
