package lib

import (
	"errors"
	"fmt"
	"strings"
)

// ParsedCommand 从整行启动命令或表单解析出的参数。
type ParsedCommand struct {
	ServerURL string
	Token     string
	LocalAddr string
	Verbose   bool
}

var (
	ErrMissingServer = errors.New("缺少 -server")
	ErrMissingToken  = errors.New("缺少 -token")
)

// ParseCommandLine 解析 `tunnel-agent -server … -token … [-local …]` 整行（可含前缀路径）。
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

// ParseFields 从三个独立字段解析（GUI 表单）。
func ParseFields(serverURL, token, local string, verbose bool) (ParsedCommand, error) {
	p := ParsedCommand{
		ServerURL: strings.TrimSpace(serverURL),
		Token:     strings.TrimSpace(token),
		LocalAddr: strings.TrimSpace(local),
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
