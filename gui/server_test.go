package gui

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestGUIStartStopAndLogs(t *testing.T) {
	ctrl := NewController()
	srv := newHTTPServer(ctrl, "http://127.0.0.1:1")
	ts := httptest.NewServer(srv.Handler)
	defer ts.Close()

	// 无真实 wss 时启动会失败，但 API 应接受请求且日志有记录。
	body, _ := json.Marshal(map[string]any{
		"server": "wss://127.0.0.1:9/nope",
		"token":  "t",
		"local":  "127.0.0.1:65530",
	})
	resp, err := http.Post(ts.URL+"/api/start", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("start status=%d", resp.StatusCode)
	}

	resp2, err := http.Get(ts.URL + "/api/logs/snapshot")
	if err != nil {
		t.Fatal(err)
	}
	defer resp2.Body.Close()
	var snap struct {
		Text string `json:"text"`
	}
	if err := json.NewDecoder(resp2.Body).Decode(&snap); err != nil {
		t.Fatal(err)
	}
	if snap.Text == "" {
		t.Fatal("启动后日志区应有内容")
	}

	resp3, err := http.Post(ts.URL+"/api/stop", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	resp3.Body.Close()
}
