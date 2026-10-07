package gui

import (
	"context"
	"fmt"
	"sync"

	"github.com/xiujiecn/tunnel-agent/agent"
	"github.com/xiujiecn/tunnel-agent/lib"
)

// Controller 管理 agent 运行与日志（GUI / 托盘共用）。
type Controller struct {
	mu     sync.Mutex
	logs   *LogBuffer
	cancel context.CancelFunc
	running bool
	lastErr string
}

func NewController() *Controller {
	return &Controller{logs: NewLogBuffer(2000)}
}

func (c *Controller) Logs() *LogBuffer { return c.logs }

func (c *Controller) Running() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.running
}

func (c *Controller) LastError() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.lastErr
}

func (c *Controller) StartFromFields(server, token, local string, verbose bool) error {
	p, err := lib.ParseFields(server, token, local, verbose)
	if err != nil {
		return err
	}
	return c.Start(agent.Config{
		ServerURL: p.ServerURL,
		Token:     p.Token,
		LocalAddr: p.LocalAddr,
		Verbose:   p.Verbose,
	})
}

func (c *Controller) StartFromPaste(line string) error {
	p, err := lib.ParseCommandLine(line)
	if err != nil {
		return err
	}
	return c.Start(agent.Config{
		ServerURL: p.ServerURL,
		Token:     p.Token,
		LocalAddr: p.LocalAddr,
		Verbose:   p.Verbose,
	})
}

func (c *Controller) Start(cfg agent.Config) error {
	c.Stop()
	ctx, cancel := context.WithCancel(context.Background())
	w := lineWriter{buf: c.logs}

	c.mu.Lock()
	c.cancel = cancel
	c.running = true
	c.lastErr = ""
	c.mu.Unlock()

	c.logs.Append(fmt.Sprintf("启动隧道：local=%s server=%s", cfg.LocalAddr, cfg.ServerURL))

	go func() {
		err := agent.Run(ctx, cfg, w)
		c.mu.Lock()
		c.running = false
		c.cancel = nil
		if err != nil && ctx.Err() == nil {
			c.lastErr = err.Error()
			c.logs.Append("错误：" + err.Error())
		} else if ctx.Err() != nil {
			c.logs.Append("已停止。")
		}
		c.mu.Unlock()
	}()
	return nil
}

func (c *Controller) Stop() {
	c.mu.Lock()
	cancel := c.cancel
	c.mu.Unlock()
	if cancel != nil {
		cancel()
	}
}
