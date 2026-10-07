package gui

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"strings"
	"time"

	"github.com/xiujiecn/tunnel-agent/lib"
)

//go:embed static/*
var staticFS embed.FS

func newHTTPServer(ctrl *Controller, baseURL string) *http.Server {
	mux := http.NewServeMux()
	sub, _ := fs.Sub(staticFS, "static")
	mux.Handle("/", http.FileServer(http.FS(sub)))

	mux.HandleFunc("/api/status", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]any{
			"running": ctrl.Running(),
			"error":   ctrl.LastError(),
		})
	})

	mux.HandleFunc("/api/logs", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		fl, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "no flush", http.StatusInternalServerError)
			return
		}
		if snap := ctrl.Logs().Snapshot(); snap != "" {
			for _, line := range strings.Split(snap, "\n") {
				fmt.Fprintf(w, "data: %s\n\n", escapeSSE(line))
			}
			fl.Flush()
		}
		ch := ctrl.Logs().Subscribe()
		defer ctrl.Logs().Unsubscribe(ch)
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
	})

	mux.HandleFunc("/api/logs/snapshot", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]string{"text": ctrl.Logs().Snapshot()})
	})

	mux.HandleFunc("/api/logs/clear", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		ctrl.Logs().Clear()
		writeJSON(w, map[string]bool{"ok": true})
	})

	type startBody struct {
		Server  string `json:"server"`
		Token   string `json:"token"`
		Local   string `json:"local"`
		Verbose bool   `json:"verbose"`
		Paste   string `json:"paste"`
	}
	mux.HandleFunc("/api/start", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		var body startBody
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
			http.Error(w, "bad json", http.StatusBadRequest)
			return
		}
		var err error
		if strings.TrimSpace(body.Paste) != "" {
			err = ctrl.StartFromPaste(body.Paste)
		} else {
			err = ctrl.StartFromFields(body.Server, body.Token, body.Local, body.Verbose)
		}
		if err != nil {
			writeJSON(w, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	})

	mux.HandleFunc("/api/parse", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
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
		writeJSON(w, map[string]any{
			"ok": true, "server": p.ServerURL, "token": p.Token, "local": p.LocalAddr, "verbose": p.Verbose,
		})
	})

	mux.HandleFunc("/api/stop", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "method", http.StatusMethodNotAllowed)
			return
		}
		ctrl.Stop()
		writeJSON(w, map[string]bool{"ok": true})
	})

	_ = baseURL
	return &http.Server{Handler: mux}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

func escapeSSE(s string) string {
	return strings.NewReplacer("\n", " ", "\r", " ").Replace(s)
}
