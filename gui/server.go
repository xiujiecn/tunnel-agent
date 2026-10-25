package gui

import (
	"embed"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/xiujiecn/tunnel-agent/agent"
	"github.com/xiujiecn/tunnel-agent/lib"
)

//go:embed static/*
var staticFS embed.FS

func newHTTPServer(ctrl *Controller, baseURL string) *http.Server {
	mux := http.NewServeMux()
	sub, _ := fs.Sub(staticFS, "static")
	mux.Handle("/", http.FileServer(http.FS(sub)))

	// 汇总：托盘/脚本可能仍读 /api/status（新增 version/buildTime/banner，旧字段保持）
	mux.HandleFunc("GET /api/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"running":   ctrl.Running(),
			"count":     ctrl.RunningCount(),
			"error":     ctrl.LastError(),
			"version":   agent.Version,
			"buildTime": agent.BuildTime,
			"banner":    agent.BannerLine(),
		})
	})

	mux.HandleFunc("GET /api/summary", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"running": ctrl.RunningCount(),
			"total":   ctrl.TotalCount(),
			"version": agent.Version,
		})
	})

	mux.HandleFunc("GET /api/tunnels", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, ctrl.List())
	})

	type tunnelBody struct {
		Server  string `json:"server"`
		Token   string `json:"token"`
		Local   string `json:"local"`
		Proto   string `json:"proto"`
		Verbose bool   `json:"verbose"`
		Paste   string `json:"paste"`
		Start   *bool  `json:"start"` // 默认 true
	}

	mux.HandleFunc("POST /api/tunnels", func(w http.ResponseWriter, r *http.Request) {
		var body tunnelBody
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		start := true
		if body.Start != nil {
			start = *body.Start
		}
		var (
			id  string
			err error
		)
		if strings.TrimSpace(body.Paste) != "" {
			id, err = ctrl.AddFromPaste(body.Paste, start)
		} else {
			id, err = ctrl.AddFromFields(body.Server, body.Token, body.Local, body.Proto, body.Verbose, start)
		}
		if err != nil {
			writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, map[string]any{"ok": true, "id": id})
	})

	mux.HandleFunc("POST /api/tunnels/{id}/start", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if err := ctrl.Start(id); err != nil {
			writeTunnelErr(w, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	})

	mux.HandleFunc("POST /api/tunnels/{id}/stop", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if err := ctrl.Stop(id); err != nil {
			writeTunnelErr(w, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	})

	mux.HandleFunc("DELETE /api/tunnels/{id}", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if err := ctrl.Remove(id); err != nil {
			writeTunnelErr(w, err)
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	})

	mux.HandleFunc("GET /api/tunnels/{id}/logs", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		buf := ctrl.LogsOf(id)
		if buf == nil {
			writeTunnelErr(w, errTunnelNotFound)
			return
		}
		serveLogSSE(w, r, buf)
	})

	mux.HandleFunc("GET /api/tunnels/{id}/logs/snapshot", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		buf := ctrl.LogsOf(id)
		if buf == nil {
			writeTunnelErr(w, errTunnelNotFound)
			return
		}
		writeJSON(w, map[string]string{"text": buf.Snapshot()})
	})

	mux.HandleFunc("POST /api/tunnels/{id}/logs/clear", func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		buf := ctrl.LogsOf(id)
		if buf == nil {
			writeTunnelErr(w, errTunnelNotFound)
			return
		}
		buf.Clear()
		writeJSON(w, map[string]bool{"ok": true})
	})

	// 控制台伪日志（托盘消息）
	mux.HandleFunc("GET /api/console/logs", func(w http.ResponseWriter, r *http.Request) {
		serveLogSSE(w, r, ctrl.Logs())
	})
	mux.HandleFunc("GET /api/console/logs/snapshot", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"text": ctrl.Logs().Snapshot()})
	})

	mux.HandleFunc("POST /api/parse", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Paste string `json:"paste"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		p, err := lib.ParseCommandLine(body.Paste)
		if err != nil {
			writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		// ★ 解析回填表单需要完整 token（用户刚从粘贴区提交）；列表 API 绝不回显完整票。
		writeJSON(w, map[string]any{
			"ok": true, "server": p.ServerURL, "token": p.Token, "local": p.LocalAddr,
			"proto": p.Proto, "verbose": p.Verbose,
		})
	})

	mux.HandleFunc("POST /api/stop-all", func(w http.ResponseWriter, r *http.Request) {
		ctrl.StopAll()
		writeJSON(w, map[string]bool{"ok": true})
	})

	_ = baseURL
	return &http.Server{Handler: mux}
}

func writeTunnelErr(w http.ResponseWriter, err error) {
	if errors.Is(err, errTunnelNotFound) {
		w.WriteHeader(http.StatusNotFound)
		writeJSON(w, map[string]any{"ok": false, "error": "隧道不存在"})
		return
	}
	w.WriteHeader(http.StatusBadRequest)
	writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
}

func serveLogSSE(w http.ResponseWriter, r *http.Request, buf *LogBuffer) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	fl, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "no flush", http.StatusInternalServerError)
		return
	}
	if snap := buf.Snapshot(); snap != "" {
		for _, line := range strings.Split(snap, "\n") {
			fmt.Fprintf(w, "data: %s\n\n", escapeSSE(line))
		}
		fl.Flush()
	}
	ch := buf.Subscribe()
	defer buf.Unsubscribe(ch)
	ctx := r.Context()
	tick := time.NewTicker(15 * time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case line, ok := <-ch:
			if !ok {
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", escapeSSE(line))
			fl.Flush()
		case <-tick.C:
			fmt.Fprintf(w, ": keepalive\n\n")
			fl.Flush()
		}
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

func escapeSSE(s string) string {
	return strings.NewReplacer("\n", " ", "\r", " ").Replace(s)
}
