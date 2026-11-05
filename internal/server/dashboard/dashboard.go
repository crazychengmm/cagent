// Package dashboard 提供 server 的只读状态面板（HTTP）。
//
// 安全边界：
//   - 仅 GET、仅展示，不提供任何写操作（命令下发/升级/token 管理仍走
//     admin gRPC + mTLS/Bearer 或 ctl）。
//   - Bearer 令牌鉴权；缺令牌一律 401，页面不泄露任何信息。
//   - 全部输出经 html/template 转义，杜绝 XSS。
//   - 建议仅绑 127.0.0.1 或经反向代理加 TLS，不直接暴露公网。
package dashboard

import (
	"context"
	"embed"
	"errors"
	"html/template"
	"net/http"
	"time"

	"github.com/crazychengmm/cagent/internal/server/store"
)

//go:embed templates/*.html
var templatesFS embed.FS

// Service 是只读面板的 HTTP 服务。
type Service struct {
	store *store.Store
	token string
	tmpl  *template.Template
}

// New 构造面板服务；token 为空会返回错误（强制鉴权）。
func New(s *store.Store, token string) (*Service, error) {
	if token == "" {
		return nil, errors.New("dashboard token 不能为空")
	}
	tmpl, err := template.New("").Funcs(template.FuncMap{
		"shortID": shortID,
	}).ParseFS(templatesFS, "templates/*.html")
	if err != nil {
		return nil, err
	}
	return &Service{store: s, token: token, tmpl: tmpl}, nil
}

// shortID 安全截断为前 n 个字符，短于 n 时原样返回（避免 slice 越界 panic）。
func shortID(id string) string {
	const n = 8
	if len(id) <= n {
		return id
	}
	return id[:n]
}

// Handler 返回带鉴权与安全头的路由。
func (s *Service) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok"))
	})
	mux.HandleFunc("/", s.auth(s.index))
	return withSecurityHeaders(mux)
}

// auth 校验 Authorization: Bearer <token>（也兼容 ?token=，便于浏览器首次访问）。
func (s *Service) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		if !s.authorized(r) {
			w.Header().Set("WWW-Authenticate", `Bearer realm="cagent-dashboard"`)
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (s *Service) authorized(r *http.Request) bool {
	const prefix = "Bearer "
	if h := r.Header.Get("Authorization"); len(h) > len(prefix) && h[:len(prefix)] == prefix {
		if subtleEqual(h[len(prefix):], s.token) {
			return true
		}
	}
	// 浏览器地址栏首次访问：?token=xxx（不写入页面链接，避免泄漏到历史/Referer）。
	if q := r.URL.Query().Get("token"); q != "" && subtleEqual(q, s.token) {
		return true
	}
	return false
}

type indexData struct {
	GeneratedAt time.Time
	Agents      []store.Agent
	Jobs        []store.Job
	Audits      []store.AuditLog
	Releases    []store.Release
	Stats       stats
}

type stats struct {
	AgentsTotal   int
	AgentsOnline  int
	AgentsOffline int
	JobsByStatus  map[string]int
}

func (s *Service) index(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()

	agents, err := s.store.ListAgents(ctx)
	if err != nil {
		http.Error(w, "查询 agent 失败", http.StatusInternalServerError)
		return
	}
	jobs, err := s.store.ListJobs(ctx, store.ListJobsFilter{Limit: 50})
	if err != nil {
		http.Error(w, "查询 job 失败", http.StatusInternalServerError)
		return
	}
	audits, err := s.store.ListAudits(ctx, 30)
	if err != nil {
		http.Error(w, "查询审计失败", http.StatusInternalServerError)
		return
	}
	releases, err := s.store.ListReleases(ctx)
	if err != nil {
		http.Error(w, "查询发布失败", http.StatusInternalServerError)
		return
	}

	st := stats{JobsByStatus: map[string]int{}}
	for _, a := range agents {
		st.AgentsTotal++
		if a.Status == store.StatusOnline {
			st.AgentsOnline++
		} else {
			st.AgentsOffline++
		}
	}
	for _, j := range jobs {
		st.JobsByStatus[j.Status]++
	}

	data := indexData{
		GeneratedAt: time.Now(),
		Agents:      agents,
		Jobs:        jobs,
		Audits:      audits,
		Releases:    releases,
		Stats:       st,
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	if err := s.tmpl.ExecuteTemplate(w, "index.html", data); err != nil {
		http.Error(w, "渲染失败", http.StatusInternalServerError)
	}
}

func withSecurityHeaders(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Content-Security-Policy",
			"default-src 'none'; style-src 'unsafe-inline'; img-src 'self' data:; base-uri 'none'; frame-ancestors 'none'")
		h.ServeHTTP(w, r)
	})
}
