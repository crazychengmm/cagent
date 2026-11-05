package e2e

import (
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

// TestDashboard_AuthAndReadonly 验证只读面板：鉴权、页面渲染、健康端点、
// POST 被拒。真实 server 子进程 + 真实 HTTP 请求。
func TestDashboard_AuthAndReadonly(t *testing.T) {
	h := New(t)
	base := "http://" + h.dashboardAddr
	client := &http.Client{Timeout: 5 * time.Second}

	// healthz 无需令牌
	resp, err := client.Get(base + "/healthz")
	if err != nil {
		t.Fatalf("healthz 请求失败: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz 应 200，实际 %d", resp.StatusCode)
	}

	// 无令牌 → 401
	resp, err = client.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("无令牌应 401，实际 %d", resp.StatusCode)
	}

	// 错误令牌 → 401
	req, _ := http.NewRequest(http.MethodGet, base+"/", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("错误令牌应 401，实际 %d", resp.StatusCode)
	}

	// 正确令牌 → 200 且含标题
	req, _ = http.NewRequest(http.MethodGet, base+"/", nil)
	req.Header.Set("Authorization", "Bearer "+h.dashboardTok)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("正确令牌应 200，实际 %d: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "cagent 状态面板") {
		t.Fatal("页面缺少标题")
	}
	if resp.Header.Get("X-Frame-Options") != "DENY" {
		t.Error("缺 X-Frame-Options 安全头")
	}

	// POST 写操作 → 405（只读保证）
	req, _ = http.NewRequest(http.MethodPost, base+"/", nil)
	req.Header.Set("Authorization", "Bearer "+h.dashboardTok)
	resp, err = client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("只读面板应拒绝 POST，实际 %d", resp.StatusCode)
	}
}

// TestDashboard_RendersAgent 验证 agent 上线后面板能看到它。
func TestDashboard_RendersAgent(t *testing.T) {
	h := New(t)
	h.StartAgent("dash1", nil)
	h.waitAgentOnline("dash1")

	req, _ := http.NewRequest(http.MethodGet, "http://"+h.dashboardAddr+"/", nil)
	req.Header.Set("Authorization", "Bearer "+h.dashboardTok)
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	out := string(body)
	if !strings.Contains(out, "dash1") || !strings.Contains(out, "online") {
		t.Fatalf("面板应展示在线 agent dash1，实际片段缺失")
	}
}
