// Package e2e 在本机以两个真实子进程（server + agent）验证全链路，
// 全程使用临时数据目录与 loopback mTLS。
package e2e

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/crazychengmm/cagent/internal/release"
)

// Harness 管理一次端到端环境。
type Harness struct {
	t        *testing.T
	Root     string
	Binary   string
	BaseDir  string
	ServerID string

	enrollAddr  string
	controlAddr string
	adminAddr   string

	privKeyPath string
	pubKeyPath  string

	serverProc *exec.Cmd
	serverOut  *outputBuffer
	agentProcs map[string]*agentProc
}

type agentProc struct {
	proc *exec.Cmd
	out  *outputBuffer
	dir  string
}

type outputBuffer struct {
	mu sync.Mutex
	b  strings.Builder
}

func (b *outputBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.b.Write(p)
	return len(p), nil
}
func (b *outputBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

// projectRoot 向上查找 go.mod 所在目录。
func projectRoot(t *testing.T) string {
	t.Helper()
	_, file, _, _ := runtime.Caller(0)
	dir := filepath.Dir(file)
	for i := 0; i < 6; i++ {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		dir = filepath.Dir(dir)
	}
	t.Fatal("找不到项目根（go.mod）")
	return ""
}

func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	return addr
}

// buildBinary 以注入版本号方式构建二进制。
func (h *Harness) buildBinary(dst, version string) {
	h.t.Helper()
	ldflags := "-X github.com/crazychengmm/cagent/internal/version.Version=" + version
	build := exec.Command("go", "build", "-ldflags", ldflags, "-o", dst, "./cmd/cagent")
	build.Dir = h.Root
	if out, err := build.CombinedOutput(); err != nil {
		h.t.Fatalf("构建二进制(%s)失败: %v\n%s", version, err, out)
	}
}

// BuildVersionedBinary 构建指定版本二进制并返回路径。
func (h *Harness) BuildVersionedBinary(name, version string) string {
	h.t.Helper()
	dst := filepath.Join(h.BaseDir, name)
	h.buildBinary(dst, version)
	return dst
}

// PublishRelease 打包、签名、发布一个版本，返回包与签名路径。
func (h *Harness) PublishRelease(version, binaryPath string) (string, string) {
	h.t.Helper()
	pkgPath := filepath.Join(h.BaseDir, "cagent-"+version+".pkg")
	h.Ctl("release", "package", "--binary", binaryPath, "--version", version, "--out", pkgPath)
	h.Ctl("release", "sign", "--key", h.privKeyPath, "--pkg", pkgPath)
	h.Ctl("release", "publish", "--pkg", pkgPath, "--sig", pkgPath+".sig")
	return pkgPath, pkgPath + ".sig"
}

// New 构建二进制并启动一个 server。
func New(t *testing.T) *Harness {
	t.Helper()
	root := projectRoot(t)
	base := t.TempDir()
	h := &Harness{
		t:           t,
		Root:        root,
		Binary:      filepath.Join(base, "cagent"),
		BaseDir:     base,
		agentProcs:  map[string]*agentProc{},
		enrollAddr:  freePort(t),
		controlAddr: freePort(t),
		adminAddr:   freePort(t),
	}
	h.buildBinary(h.Binary, "1.0.0")
	h.generateSigningKey()
	h.startServer()
	return h
}

// PrivKeyPath/PubKeyPath 返回发布签名密钥路径。
func (h *Harness) PrivKeyPath() string { return h.privKeyPath }
func (h *Harness) PubKeyPath() string  { return h.pubKeyPath }

func (h *Harness) generateSigningKey() {
	priv, pub, err := release.GenerateSigningKey()
	if err != nil {
		h.t.Fatal(err)
	}
	privPEM, err := release.EncodePrivateKeyPEM(priv)
	if err != nil {
		h.t.Fatal(err)
	}
	pubPEM, err := release.EncodePublicKeyPEM(pub)
	if err != nil {
		h.t.Fatal(err)
	}
	h.privKeyPath = filepath.Join(h.BaseDir, "release.key")
	h.pubKeyPath = filepath.Join(h.BaseDir, "release.pub")
	if err := os.WriteFile(h.privKeyPath, privPEM, 0o600); err != nil {
		h.t.Fatal(err)
	}
	if err := os.WriteFile(h.pubKeyPath, pubPEM, 0o644); err != nil {
		h.t.Fatal(err)
	}
}

func (h *Harness) startServer() {
	dataDir := filepath.Join(h.BaseDir, "server-data")
	os.MkdirAll(dataDir, 0o700)
	cfgPath := filepath.Join(h.BaseDir, "server.yaml")
	cfg := fmt.Sprintf(`
enrollment_listen: "%s"
control_listen: "%s"
admin_listen: "%s"
data_dir: "%s"
server_cert_hosts: ["127.0.0.1", "localhost"]
heartbeat_timeout: 5s
queue_ttl: 60s
agent_cert_validity: 90d
enrollment_token_ttl: 168h
release_signing_pubkey_file: "%s"
`, h.enrollAddr, h.controlAddr, h.adminAddr, dataDir, h.pubKeyPath)
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		h.t.Fatal(err)
	}

	h.serverOut = &outputBuffer{}
	cmd := exec.Command(h.Binary, "server", "--config", cfgPath)
	cmd.Stdout = h.serverOut
	cmd.Stderr = h.serverOut
	if err := cmd.Start(); err != nil {
		h.t.Fatalf("启动 server: %v", err)
	}
	h.serverProc = cmd
	h.t.Cleanup(h.Stop)

	// 等待三个端口可连（CA 生成需要一点时间）。
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if conn, err := net.DialTimeout("tcp", h.adminAddr, 300*time.Millisecond); err == nil {
			conn.Close()
			return
		}
		time.Sleep(150 * time.Millisecond)
	}
	h.t.Fatalf("server 未就绪\n%s", h.serverOut.String())
}

// Stop 终止所有子进程。
func (h *Harness) Stop() {
	for _, a := range h.agentProcs {
		_ = a.proc.Process.Kill()
	}
	if h.serverProc != nil {
		_ = h.serverProc.Process.Kill()
	}
}

// ServerLog 返回 server 日志。
func (h *Harness) ServerLog() string { return h.serverOut.String() }

// AdminToken 读取 server 首启生成的 admin token。
func (h *Harness) AdminToken() string {
	data, err := os.ReadFile(filepath.Join(h.BaseDir, "server-data", "admin_token"))
	if err != nil {
		h.t.Fatal(err)
	}
	return strings.TrimSpace(string(data))
}

// CAPath 返回 server CA 证书路径。
func (h *Harness) CAPath() string {
	return filepath.Join(h.BaseDir, "server-data", "ca", "ca.crt")
}

// CtlConfigPath 生成一份 ctl 配置并返回路径。
func (h *Harness) CtlConfigPath() string {
	path := filepath.Join(h.BaseDir, "ctl.yaml")
	content := fmt.Sprintf("admin_address: %q\nadmin_token: %q\nserver_ca_file: %q\n",
		h.adminAddr, h.AdminToken(), h.CAPath())
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		h.t.Fatal(err)
	}
	return path
}

// Ctl 执行 ctl 子命令并返回 stdout。
func (h *Harness) Ctl(args ...string) string {
	h.t.Helper()
	args = append([]string{"--config", h.CtlConfigPath()}, args...)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, h.Binary, append([]string{"ctl"}, args...)...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		h.t.Fatalf("ctl %v 失败: %v\n%s", args, err, string(out))
	}
	return string(out)
}

// CreateEnrollmentToken 用 ctl 创建 token 并返回明文。
func (h *Harness) CreateEnrollmentToken() string {
	out := h.Ctl("token", "create", "--ttl", "24", "--description", "e2e")
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "cagt_") {
			return strings.TrimSpace(line)
		}
	}
	h.t.Fatalf("未从输出解析到 token: %s", out)
	return ""
}

// StartAgent 用给定标签启动一个 agent，返回其 agent id 目录名。
func (h *Harness) StartAgent(name string, labels map[string]string) {
	h.t.Helper()
	agentDir := filepath.Join(h.BaseDir, "agent-"+name)
	os.MkdirAll(agentDir, 0o700)
	token := h.CreateEnrollmentToken()

	// 每个 agent 使用独立二进制副本，升级只替换该副本。
	binCopy := filepath.Join(agentDir, "bin", "cagent")
	os.MkdirAll(filepath.Dir(binCopy), 0o755)
	copyFileH(h.t, h.Binary, binCopy)

	// 非 systemd 重启：内联命令由 cagent 经 sh -c 执行，$PPID 即 cagent 自身。
	// 拉起同副本新进程后 SIGTERM 当前进程。
	agentCfgPath := filepath.Join(agentDir, "config.yaml")
	restartLog := filepath.Join(agentDir, "restart.log")
	restartCmd := fmt.Sprintf(
		`nohup '%s' agent --config '%s' >>'%s' 2>&1 & kill -TERM $PPID`,
		binCopy, agentCfgPath, restartLog)

	if labels == nil {
		labels = map[string]string{}
	}
	labels["name"] = name // 注入可区分标签
	labelYAML := ""
	for k, v := range labels {
		labelYAML += fmt.Sprintf("  %s: %q\n", k, v)
	}
	cfg := fmt.Sprintf(`
enrollment_address: "%s"
control_address: "%s"
enrollment_token: "%s"
server_ca_file: "%s"
signing_pubkey_file: "%s"
restart_command: "%s"
data_dir: "%s"
heartbeat_interval: 200ms
job_concurrency: 2
labels:
%s`, h.enrollAddr, h.controlAddr, token, h.CAPath(), h.pubKeyPath,
		restartCmd, agentDir, labelYAML)
	cfgPath := filepath.Join(agentDir, "config.yaml")
	if err := os.WriteFile(cfgPath, []byte(cfg), 0o600); err != nil {
		h.t.Fatal(err)
	}

	out := &outputBuffer{}
	cmd := exec.Command(binCopy, "agent", "--config", cfgPath)
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Start(); err != nil {
		h.t.Fatalf("启动 agent: %v\n%s", err, out.String())
	}
	h.agentProcs[name] = &agentProc{proc: cmd, out: out, dir: agentDir}
}

func copyFileH(t *testing.T, src, dst string) {
	t.Helper()
	in, err := os.Open(src)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	out, err := os.Create(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer out.Close()
	if _, err := io.Copy(out, in); err != nil {
		t.Fatal(err)
	}
	if err := out.Chmod(0o755); err != nil {
		t.Fatal(err)
	}
}

// KillAgent 强杀指定 agent。
func (h *Harness) KillAgent(name string) {
	a, ok := h.agentProcs[name]
	if !ok {
		h.t.Fatalf("未知 agent %s", name)
	}
	_ = a.proc.Process.Kill()
	a.proc.Wait()
}

// StartAgentProcess 重新启动同名 agent（身份已存在则直接连接）。
func (h *Harness) StartAgentProcess(name string) {
	a := h.agentProcs[name]
	cfgPath := filepath.Join(a.dir, "config.yaml")
	binCopy := filepath.Join(a.dir, "bin", "cagent")
	out := a.out
	cmd := exec.Command(binCopy, "agent", "--config", cfgPath)
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Start(); err != nil {
		h.t.Fatalf("重启 agent: %v", err)
	}
	a.proc = cmd
}

// AgentLog 返回 agent 日志。
func (h *Harness) AgentLog(name string) string {
	return h.agentProcs[name].out.String()
}

// AgentDataDir 返回 agent 数据目录。
func (h *Harness) AgentDataDir(name string) string {
	return h.agentProcs[name].dir
}

// DumpAgent 在测试输出中打印 agent 现场（调试用）。
func (h *Harness) DumpAgent(name string) {
	if !h.t.Failed() {
		return
	}
	a := h.agentProcs[name]
	if a == nil {
		return
	}
	h.t.Logf("==== agent %s 进程日志 ====\n%s", name, a.out.String())
	for _, f := range []string{"restart.log", "guard.log", "upgrade-state.json"} {
		path := filepath.Join(a.dir, f)
		if data, err := os.ReadFile(path); err == nil {
			h.t.Logf("==== %s ====\n%s", f, string(data))
		}
	}
	for _, f := range []string{"releases/cagent.old", "releases/cagent.staged",
		"upgrade-healthy", "upgrade-state.json", "upgrade-result.json",
		"guard.log", "restart.log", "probe-debug.log"} {
		path := filepath.Join(a.dir, f)
		if data, err := os.ReadFile(path); err == nil {
			preview := string(data)
			if len(preview) > 500 {
				preview = preview[:500]
			}
			h.t.Logf("==== %s (%d bytes) ====\n%s", f, len(data), preview)
		} else {
			h.t.Logf("==== %s 不存在 ====", f)
		}
	}
	// 当前运行二进制版本
	if out, err := exec.Command(filepath.Join(a.dir, "bin", "cagent"), "--version").CombinedOutput(); err == nil {
		h.t.Logf("==== bin/cagent --version ====\n%s", string(out))
	}
	if pidBytes, err := os.ReadFile(filepath.Join(a.dir, "guard.pid")); err == nil {
		pid := strings.TrimSpace(string(pidBytes))
		out, _ := exec.Command("ps", "-o", "pid,ppid,pgid,stat,command", "-p", pid).CombinedOutput()
		h.t.Logf("==== guard pid %s 状态 ====\n%s", pid, string(out))
		if sample, err := exec.Command("sample", pid, "1", "-mayDie").CombinedOutput(); err == nil {
			h.t.Logf("==== guard sample ====\n%s", string(sample))
		}
	}
}

// WaitFor 轮询直到条件成立。
func (h *Harness) WaitFor(timeout time.Duration, cond func() bool, msg string) {
	h.t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	h.t.Fatalf("等待超时: %s\nserver log:\n%s", msg, h.ServerLog())
}
