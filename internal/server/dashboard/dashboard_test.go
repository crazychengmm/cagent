package dashboard

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crazychengmm/cagent/internal/server/store"
)

func openStore(t *testing.T) *store.Store {
	t.Helper()
	s, err := store.Open(context.Background(), filepath.Join(t.TempDir(), "dash.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestNew_RejectsEmptyToken(t *testing.T) {
	t.Parallel()
	s := openStore(t)
	if _, err := New(s, ""); err == nil {
		t.Fatal("空 token 必须报错，禁止无鉴权面板")
	}
}

func TestAuth_RejectsMissingAndWrongToken(t *testing.T) {
	t.Parallel()
	s := openStore(t)
	svc, err := New(s, "secret-token")
	if err != nil {
		t.Fatal(err)
	}
	h := svc.Handler()

	// 无 Authorization 头 → 401
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("无令牌应 401，实际 %d", rec.Code)
	}

	// 错误令牌 → 401
	req = httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer wrong")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("错误令牌应 401，实际 %d", rec.Code)
	}
}

func TestAuth_AcceptsCorrectTokenAndRenders(t *testing.T) {
	t.Parallel()
	s := openStore(t)
	now := time.Now().UTC()
	if err := s.InsertAgent(context.Background(), store.Agent{
		AgentID: "ag-1", Hostname: "web-1", Labels: map[string]string{"env": "prod"},
		Status: store.StatusOnline, Version: "1.0.0",
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	svc, err := New(s, "secret-token")
	if err != nil {
		t.Fatal(err)
	}
	h := svc.Handler()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer secret-token")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("正确令牌应 200，实际 %d: %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, want := range []string{"cagent 状态面板", "web-1", "1.0.0", "online"} {
		if !strings.Contains(body, want) {
			t.Errorf("页面应包含 %q", want)
		}
	}
}

func TestQueryToken_AndSecurityHeaders(t *testing.T) {
	t.Parallel()
	s := openStore(t)
	svc, _ := New(s, "tok")
	h := svc.Handler()

	// ?token= 也可用于浏览器首次访问
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/?token=tok", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("query token 应放行，实际 %d", rec.Code)
	}
	if got := rec.Header().Get("X-Frame-Options"); got != "DENY" {
		t.Errorf("缺 X-Frame-Options: %s", got)
	}
	if got := rec.Header().Get("Content-Security-Policy"); got == "" {
		t.Error("缺 CSP 头")
	}

	// healthz 不需要令牌（供存活探针）
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if rec2.Code != http.StatusOK {
		t.Errorf("healthz 应 200，实际 %d", rec2.Code)
	}
}

func TestRendersJobWithExitCode(t *testing.T) {
	t.Parallel()
	s := openStore(t)
	now := time.Now().UTC()
	if err := s.InsertAgent(context.Background(), store.Agent{
		AgentID: "ag-job", Hostname: "h", Labels: map[string]string{},
		Status: store.StatusOnline, CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	code := 0
	if err := s.CreateJob(context.Background(), store.Job{
		JobID: "job-1", AgentID: "ag-job", Kind: store.KindCommand,
		Command: "echo x", RequestedBy: "admin", TimeoutSeconds: 30,
		CreatedAt: now, ExitCode: &code, Status: store.JobStatusSucceeded,
	}); err != nil {
		t.Fatal(err)
	}
	svc, _ := New(s, "tok")
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer tok")
	svc.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("应 200，实际 %d: %s", rec.Code, rec.Body.String())
	}
	if !strings.Contains(rec.Body.String(), "succeeded") {
		t.Error("页面应渲染 job 状态")
	}
}

func TestPostMethodRejected(t *testing.T) {
	t.Parallel()
	s := openStore(t)
	svc, _ := New(s, "tok")
	h := svc.Handler()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/", nil)
	req.Header.Set("Authorization", "Bearer tok")
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusMethodNotAllowed {
		t.Errorf("只读面板应拒绝 POST，实际 %d", rec.Code)
	}
}

func TestXSS_Escaped(t *testing.T) {
	t.Parallel()
	s := openStore(t)
	now := time.Now().UTC()
	// 主机名含脚本，模板必须转义。
	if err := s.InsertAgent(context.Background(), store.Agent{
		AgentID: "ag-x", Hostname: `<script>alert(1)</script>`,
		Labels: map[string]string{}, Status: store.StatusOffline,
		CreatedAt: now, UpdatedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	svc, _ := New(s, "tok")
	h := svc.Handler()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer tok")
	h.ServeHTTP(rec, req)
	body := rec.Body.String()
	if strings.Contains(body, "<script>alert(1)</script>") {
		t.Fatal("主机名未被 HTML 转义，存在 XSS")
	}
	if !strings.Contains(body, "&lt;script&gt;") {
		t.Fatal("期望看到转义后的实体")
	}
}
