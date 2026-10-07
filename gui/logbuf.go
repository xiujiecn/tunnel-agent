package gui

import (
	"strings"
	"sync"
)

// LogBuffer 线程安全的运行日志（GUI 实时展示 + 复制）。
type LogBuffer struct {
	mu      sync.Mutex
	lines   []string
	max     int
	waiters map[chan string]struct{}
}

func NewLogBuffer(maxLines int) *LogBuffer {
	if maxLines <= 0 {
		maxLines = 2000
	}
	return &LogBuffer{max: maxLines, waiters: make(map[chan string]struct{})}
}

func (b *LogBuffer) Append(text string) {
	text = strings.TrimRight(text, "\r\n")
	if text == "" {
		return
	}
	b.mu.Lock()
	for _, line := range strings.Split(text, "\n") {
		if line == "" {
			continue
		}
		b.lines = append(b.lines, line)
		if len(b.lines) > b.max {
			b.lines = b.lines[len(b.lines)-b.max:]
		}
		for ch := range b.waiters {
			select {
			case ch <- line:
			default:
			}
		}
	}
	b.mu.Unlock()
}

func (b *LogBuffer) Snapshot() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return strings.Join(b.lines, "\n")
}

func (b *LogBuffer) Clear() {
	b.mu.Lock()
	b.lines = nil
	b.mu.Unlock()
}

func (b *LogBuffer) Subscribe() chan string {
	ch := make(chan string, 64)
	b.mu.Lock()
	b.waiters[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

func (b *LogBuffer) Unsubscribe(ch chan string) {
	b.mu.Lock()
	delete(b.waiters, ch)
	close(ch)
	b.mu.Unlock()
}

type lineWriter struct {
	buf *LogBuffer
}

func (w lineWriter) Write(p []byte) (int, error) {
	w.buf.Append(string(p))
	return len(p), nil
}
