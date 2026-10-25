package gui

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/xiujiecn/tunnel-agent/agent"
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
		Running int    `json:"running"`
		Total   int    `json:"total"`
		Version string `json:"version"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&sum); err != nil {
		t.Fatal(err)
	}
	if sum.Running != 0 || sum.Total != 0 {
		t.Fatalf("空态 summary=%+v", sum)
	}
	if sum.Version != agent.Version {
		t.Fatalf("summary.version=%q，期望 %q", sum.Version, agent.Version)
	}
}

// TestAPIStatusExposesVersion /api/status 必须带回 agent 真源版本字段。
// 破坏性：从 writeJSON 里删掉 version ⇒ 本用例必须红。
func TestAPIStatusExposesVersion(t *testing.T) {
	ctrl := NewController()
	srv := newHTTPServer(ctrl, "http://127.0.0.1:1")
	ts := httptest.NewServer(srv.Handler)
	defer ts.Close()

	resp, err := http.Get(ts.URL + "/api/status")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var st map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&st); err != nil {
		t.Fatal(err)
	}
	ver, _ := st["version"].(string)
	bt, _ := st["buildTime"].(string)
	banner, _ := st["banner"].(string)
	if ver != agent.Version {
		t.Fatalf("version=%q，期望 %q", ver, agent.Version)
	}
	if bt != agent.BuildTime {
		t.Fatalf("buildTime=%q，期望 %q", bt, agent.BuildTime)
	}
	if !strings.Contains(banner, agent.Version) {
		t.Fatalf("banner 应含版本串，got %q", banner)
	}
	if banner != agent.BannerLine() {
		t.Fatalf("banner=%q，期望 %q", banner, agent.BannerLine())
	}
}

// TestConsoleFooterBeforeVersionScript 回归护栏（v0.3.6 现场缺陷）：
// 控制台把 <p id="footerBanner"> 写在 </script> 之后时，loadVersion() 执行到
// foot.textContent 会抛空引用 ⇒ 被 catch 兜回「版本未知」⇒ 版本号永远不显示。
// 判据：页脚元素必须出现在 <script> 之前。
func TestConsoleFooterBeforeVersionScript(t *testing.T) {
	raw, err := os.ReadFile("static/index.html")
	if err != nil {
		t.Fatalf("读 static/index.html 失败: %v", err)
	}
	s := string(raw)
	foot := strings.Index(s, `id="footerBanner"`)
	script := strings.Index(s, "<script>")
	if foot < 0 || script < 0 {
		t.Fatalf("找不到 footerBanner(%d) 或 <script>(%d)", foot, script)
	}
	if foot > script {
		t.Fatalf("footerBanner 出现在 <script> 之后（foot=%d script=%d）：loadVersion() 会因 foot 为 null 抛错，版本号显示为「版本未知」", foot, script)
	}
	// 守卫语句必须在，避免以后 DOM 顺序再变又把版本号吞掉
	if !strings.Contains(s, "if (foot) foot.textContent") {
		t.Fatalf("缺少 foot 空值守卫：应写成 `if (foot) foot.textContent = ...`")
	}
}
