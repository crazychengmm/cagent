package e2e

import (
	"regexp"
	"strings"
	"testing"
	"time"
)

func (h *Harness) waitAgentOnline(name string) {
	h.t.Helper()
	h.WaitFor(15*time.Second, func() bool {
		out := h.Ctl("agent", "list")
		return strings.Contains(out, "name="+name) && strings.Contains(out, "online")
	}, "agent "+name+" online")
}

func jobIDsFromRun(out string) []string {
	var ids []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "job_") {
			ids = append(ids, line)
		}
	}
	return ids
}

// TestE2E_RunJobOutputAndExit 验证任务下发→执行→输出/退出码回收。
func TestE2E_RunJobOutputAndExit(t *testing.T) {
	h := New(t)
	h.StartAgent("run1", map[string]string{"env": "test"})
	h.waitAgentOnline("run1")

	out := h.Ctl("job", "run", "--selector", "name=run1", "--timeout", "30",
		"printf e2e-ok; echo; exit 3")
	ids := jobIDsFromRun(out)
	if len(ids) != 1 {
		t.Fatalf("应返回 1 个 job id: %s", out)
	}
	jobID := ids[0]

	h.WaitFor(10*time.Second, func() bool {
		return strings.Contains(h.Ctl("job", "show", jobID), "failed")
	}, "job 终态 failed(exit 3)")

	show := h.Ctl("job", "show", jobID)
	if !strings.Contains(show, "e2e-ok") {
		t.Errorf("输出未回收:\n%s", show)
	}
	if !regexp.MustCompile(`退出码:\s+3`).MatchString(show) {
		t.Errorf("退出码应为 3:\n%s", show)
	}
}

// TestE2E_CancelJob 验证取消终止在跑任务。
func TestE2E_CancelJob(t *testing.T) {
	h := New(t)
	h.StartAgent("run2", nil)
	h.waitAgentOnline("run2")

	out := h.Ctl("job", "run", "--selector", "name=run2", "--timeout", "60", "sleep 300")
	jobID := jobIDsFromRun(out)[0]

	h.WaitFor(5*time.Second, func() bool {
		return strings.Contains(h.Ctl("job", "list", "--agent", firstAgentID(h, jobID)), "running")
	}, "job running")

	h.Ctl("job", "cancel", jobID)
	h.WaitFor(10*time.Second, func() bool {
		return strings.Contains(h.Ctl("job", "show", jobID), "canceled")
	}, "job canceled")
}

func firstAgentID(h *Harness, jobID string) string {
	out := h.Ctl("job", "show", jobID)
	for _, line := range strings.Split(out, "\n") {
		if strings.HasPrefix(line, "AGENT:") {
			return strings.TrimSpace(strings.TrimPrefix(line, "AGENT:"))
		}
	}
	return ""
}
