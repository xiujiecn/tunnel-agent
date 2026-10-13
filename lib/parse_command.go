package lib

import (
	"errors"
	"fmt"
	"strings"

	"github.com/xiujiecn/tunnel-agent/agent"
)

// ParsedCommand 从整行启动命令或表单解析出的参数。
type ParsedCommand struct {
	ServerURL string
	Token     string
	LocalAddr string
	// Proto 协议：http（默认）| tcp | udp。
	//
	// ★ 必须解析它：下载页生成的启动命令带 `-proto tcp`，
	//   而 GUI 的「粘贴解析」原先只认 -server/-token/-local/-v，
	//   于是整行一贴就报「未知参数 "-proto"」，GUI 里根本起不了 TCP 隧道。
	Proto   string
	Verbose bool
}

var (
	ErrMissingServer = errors.New("缺少 -server")
	ErrMissingToken  = errors.New("缺少 -token")
)

// ParseProto 校验 -proto 取值并归一化；空串按 http。
//
// ★ 未知值要报错、不要静默兜底成 http —— 否则把 tcp 手误打成 tcpp 时
// 会安静地跑成 HTTP 隧道，用户只会看到「连上了但数据不通」这种更难查的现象。
func ParseProto(raw string) (string, error) {
	v := strings.ToLower(strings.TrimSpace(raw))
	switch v {
	case "", "http", "tcp", "udp":
		return agent.NormalizeProto(v), nil
	default:
		return "", fmt.Errorf("-proto 只支持 http|tcp|udp，收到 %q", strings.TrimSpace(raw))
	}
}

// ParseCommandLine 解析 `tunnel-agent -server … -token … [-local …] [-proto …] [-v]` 整行（可含前缀路径）。
func ParseCommandLine(line string) (ParsedCommand, error) {
	args := tokenizeCommandLine(strings.TrimSpace(line))
	if len(args) == 0 {
		return ParsedCommand{}, fmt.Errorf("空命令")
	}
	// 去掉可选的 tunnel-agent / ./tunnel-agent 前缀
	if base := args[0]; base == "tunnel-agent" || strings.HasSuffix(base, "tunnel-agent") || strings.HasSuffix(base, "tunnel-agent.exe") {
		args = args[1:]
	}
	return parseFlags(args)
}

// ParseFields 从独立字段解析（GUI 表单）。
func ParseFields(serverURL, token, local, proto string, verbose bool) (ParsedCommand, error) {
	protoNorm, err := ParseProto(proto)
	if err != nil {
		return ParsedCommand{}, err
	}
	p := ParsedCommand{
		ServerURL: strings.TrimSpace(serverURL),
		Token:     strings.TrimSpace(token),
		LocalAddr: strings.TrimSpace(local),
		Proto:     protoNorm,
		Verbose:   verbose,
	}
	if p.LocalAddr == "" {
		p.LocalAddr = "127.0.0.1:3000"
	}
	if p.ServerURL == "" {
		return ParsedCommand{}, ErrMissingServer
	}
	if p.Token == "" {
		return ParsedCommand{}, ErrMissingToken
	}
	return p, nil
}

func parseFlags(args []string) (ParsedCommand, error) {
	var p ParsedCommand
	p.LocalAddr = "127.0.0.1:3000"
	p.Proto = agent.NormalizeProto("")
	for i := 0; i < len(args); i++ {
		a := args[i]
		switch a {
		case "-v", "--v":
			p.Verbose = true
		case "-server", "--server":
			if i+1 >= len(args) {
				return ParsedCommand{}, ErrMissingServer
			}
			i++
			p.ServerURL = args[i]
		case "-token", "--token":
			if i+1 >= len(args) {
				return ParsedCommand{}, ErrMissingToken
			}
			i++
			p.Token = args[i]
		case "-local", "--local":
			if i+1 >= len(args) {
				return ParsedCommand{}, errors.New("缺少 -local 参数值")
			}
			i++
			p.LocalAddr = args[i]
		case "-proto", "--proto":
			if i+1 >= len(args) {
				return ParsedCommand{}, errors.New("缺少 -proto 参数值")
			}
			i++
			v, err := ParseProto(args[i])
			if err != nil {
				return ParsedCommand{}, err
			}
			p.Proto = v
		default:
			if strings.HasPrefix(a, "-") {
				return ParsedCommand{}, fmt.Errorf("未知参数 %q", a)
			}
		}
	}
	if p.ServerURL == "" {
		return ParsedCommand{}, ErrMissingServer
	}
	if p.Token == "" {
		return ParsedCommand{}, ErrMissingToken
	}
	return p, nil
}

// tokenizeCommandLine 简易分词（支持双引号）。
func tokenizeCommandLine(s string) []string {
	var out []string
	var b strings.Builder
	inQuote := false
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '"':
			inQuote = !inQuote
		case (c == ' ' || c == '\t') && !inQuote:
			if b.Len() > 0 {
				out = append(out, b.String())
				b.Reset()
			}
		default:
			b.WriteByte(c)
		}
	}
	if b.Len() > 0 {
		out = append(out, b.String())
	}
	return out
}
