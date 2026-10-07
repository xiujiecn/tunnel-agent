package agent

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/gorilla/websocket"
)

// ErrFatalConnect 致命连接错误：再重连也不会好（票据已废 / 隧道不可用）。
// agent 必须停止退避重连，并提示用户去页面换发。
var ErrFatalConnect = errors.New("fatal connect error")

// fatalTicketHints 服务端 HTTP body / close reason 里的机器码（与 ClassifyAgentTicketError 对齐）。
var fatalTicketHints = []string{
	"ticket revoked",
	"ticket mismatch",
	"ticket invalid",
	"tunnel closed",
	// 旧文案兼容（升级前日志/缓存）
	"ticket already used",
	"ticket expired",
}

var fatalCloseHints = []string{
	"agent already attached",
	"tunnel closed",
	"tunnel unavailable",
}

// classifyDialError 把 Dial 失败（含握手 401 body）分类为致命或瞬时。
func classifyDialError(err error, resp *http.Response) error {
	if resp != nil && resp.Body != nil {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		_ = resp.Body.Close()
		text := strings.TrimSpace(string(body))
		if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
			if text == "" {
				text = fmt.Sprintf("HTTP %d", resp.StatusCode)
			}
			if isFatalTicketText(text) || resp.StatusCode == http.StatusUnauthorized {
				return fmt.Errorf("%w: %s（请到秀杰工作台「内网穿透」页点「换发启动命令」）", ErrFatalConnect, text)
			}
			return fmt.Errorf("%w: 服务端拒绝连接（HTTP %d）：%s", ErrFatalConnect, resp.StatusCode, text)
		}
		if text != "" {
			return fmt.Errorf("服务端拒绝连接（HTTP %d）：%s", resp.StatusCode, text)
		}
	}
	if err == nil {
		return errors.New("连接服务端失败")
	}
	// gorilla 在握手失败时也可能把状态码塞进错误字符串。
	msg := err.Error()
	if isFatalTicketText(msg) {
		return fmt.Errorf("%w: %s（请到秀杰工作台「内网穿透」页点「换发启动命令」）", ErrFatalConnect, msg)
	}
	return fmt.Errorf("连接服务端失败：%w", err)
}

// classifyServeError 把已建立连接后的断开分类。
func classifyServeError(err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, errTunnelClosed) {
		return fmt.Errorf("%w: 隧道已被关闭（服务端通知）", ErrFatalConnect)
	}
	var ce *websocket.CloseError
	if errors.As(err, &ce) {
		switch ce.Code {
		case 4001, 4002, 4003, 4004:
			reason := ce.Text
			if reason == "" {
				reason = fmt.Sprintf("close %d", ce.Code)
			}
			return fmt.Errorf("%w: %s（请到秀杰工作台「内网穿透」页点「换发启动命令」）", ErrFatalConnect, reason)
		case 4005, 4006:
			reason := ce.Text
			if reason == "" {
				reason = fmt.Sprintf("close %d", ce.Code)
			}
			return fmt.Errorf("%w: %s", ErrFatalConnect, reason)
		}
		if isFatalTicketText(ce.Text) || isFatalCloseText(ce.Text) {
			return fmt.Errorf("%w: %s", ErrFatalConnect, ce.Text)
		}
	}
	msg := err.Error()
	if isFatalTicketText(msg) || isFatalCloseText(msg) {
		return fmt.Errorf("%w: %s", ErrFatalConnect, msg)
	}
	return err
}

func isFatalTicketText(s string) bool {
	low := strings.ToLower(s)
	for _, h := range fatalTicketHints {
		if strings.Contains(low, h) {
			return true
		}
	}
	return false
}

func isFatalCloseText(s string) bool {
	low := strings.ToLower(s)
	for _, h := range fatalCloseHints {
		if strings.Contains(low, h) {
			return true
		}
	}
	return false
}
