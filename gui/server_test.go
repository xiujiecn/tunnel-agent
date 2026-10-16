package gui

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

const fullTokenForAPI = "secret-token-ABCDEFGH-12345678"

func TestAPITunnelsCollectionAndTokenMask(t *testing.T) {
	ctrl := NewController()
	srv := newHTTPServer(ctrl, "http://127.0.0.1:1")
	ts := httptest.NewServer(srv.Handler)
	defer ts.Close()

	body, _ := json.Marshal(map[string]any{
		"server": "wss://127.0.0.1:9/nope",
		"token":  fullTokenForAPI,
		"local":  "127.0.0.1:65530",
		"proto":  "http",
		"start":  true,
	})
	resp, err := http.Post(ts.URL+"/api/tunnels", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("POST /api/tunnels status=%d body=%s", resp.StatusCode, raw)
	}
	var created struct {
		OK  bool   `json:"ok"`
		ID  string `json:"id"`
		Err string `json:"error"`
	}
	if err := json.Unmarshal(raw, &created); err != nil {
		t.Fatal(err)
	}
	if !created.OK || created.ID == "" {
		t.Fatalf("create failed: %+v", created)
	}

	// ★ GET /api/tunnels 响应体绝不能含完整票据
	listResp, err := http.Get(ts.URL + "/api/tunnels")
	if err != nil {
		t.Fatal(err)
	}
	defer listResp.Body.Close()
	listRaw, _ := io.ReadAll(listResp.Body)
	if strings.Contains(string(listRaw), fullTokenForAPI) {
		t.Fatalf("GET /api/tunnels 泄漏完整 token：%s", listRaw)
	}
	var list []TunnelInfo
	if err := json.Unmarshal(listRaw, &list); err != nil {
		t.Fatal(err)
	}
	if len(list) != 1 {
		t.Fatalf("期望 1 条，got %d", len(list))
	}
	if list[0].Token == fullTokenForAPI {
		t.Fatal("token 字段必须是掩码，不能是完整票据")
	}
	if !strings.Contains(list[0].Token, "…") && list[0].Token != "********" {
		t.Fatalf("期望掩码形态，got %q", list[0].Token)
	}

	// 第二条，再停第一条，另一条不受影响（经 HTTP）
	body2, _ := json.Marshal(map[string]any{
		"server": "wss://127.0.0.1:9/nope2",
		"token":  "another-token-ZZZZYYYY-9999",
		"local":  "127.0.0.1:65529",
		"proto":  "tcp",
		"start":  true,
	})
	resp2, err := http.Post(ts.URL+"/api/tunnels", "application/json", bytes.NewReader(body2))
	if err != nil {
		t.Fatal(err)
	}
	var created2 struct {
		OK bool   `json:"ok"`
		ID string `json:"id"`
	}
	_ = json.NewDecoder(resp2.Body).Decode(&created2)
	resp2.Body.Close()
	if !created2.OK {
		t.Fatal("第二条创建失败")
	}

	waitRunning := func(want int) {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if ctrl.RunningCount() >= want {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("等待 running>=%d 超时，got %d", want, ctrl.RunningCount())
	}
	waitRunning(2)

	stopResp, err := http.Post(ts.URL+"/api/tunnels/"+created.ID+"/stop", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	stopResp.Body.Close()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		a, _ := ctrl.Get(created.ID)
		b, _ := ctrl.Get(created2.ID)
		if !a.Running && b.Running {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	a, _ := ctrl.Get(created.ID)
	b, _ := ctrl.Get(created2.ID)
	if a.Running || !b.Running {
		t.Fatalf("停 1 条后：a.running=%v b.running=%v（b 必须仍在跑）", a.Running, b.Running)
	}

	delResp, err := http.NewRequest(http.MethodDelete, ts.URL+"/api/tunnels/"+created.ID, nil)
	if err != nil {
		t.Fatal(err)
	}
	delHTTP, err := http.DefaultClient.Do(delResp)
	if err != nil {
		t.Fatal(err)
	}
	delHTTP.Body.Close()
	if n := len(ctrl.List()); n != 1 {
		t.Fatalf("DELETE 后应剩 1，got %d", n)
	}

	// 404 分支
	badStop, err := http.Post(ts.URL+"/api/tunnels/does-not-exist/stop", "application/json", nil)
	if err != nil {
		t.Fatal(err)
	}
	defer badStop.Body.Close()
	if badStop.StatusCode != http.StatusNotFound {
		t.Fatalf("不存在的 id stop 期望 404，got %d", badStop.StatusCode)
	}
	var badBody map[string]any
	_ = json.NewDecoder(badStop.Body).Decode(&badBody)
	if badBody["error"] != "隧道不存在" {
		t.Fatalf("404 错误文案：%v", badBody)
	}

	badDelReq, _ := http.NewRequest(http.MethodDelete, ts.URL+"/api/tunnels/does-not-exist", nil)
	badDel, err := http.DefaultClient.Do(badDelReq)
	if err != nil {
		t.Fatal(err)
	}
	defer badDel.Body.Close()
	if badDel.StatusCode != http.StatusNotFound {
		t.Fatalf("不存在的 id DELETE 期望 404，got %d", badDel.StatusCode)
	}

	ctrl.StopAll()
}

func TestAPISummary(t *testing.T) {
	ctrl := NewController()
	srv := newHTTPServer(ctrl, "http://127.0.0.1:1")
	ts := httptest.NewServer(srv.Handler)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/summary")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var sum struct {
		Running int `json:"running"`
		Total   int `json:"total"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&sum); err != nil {
		t.Fatal(err)
	}
	if sum.Running != 0 || sum.Total != 0 {
		t.Fatalf("空态 summary=%+v", sum)
	}
}
