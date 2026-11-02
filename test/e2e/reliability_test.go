package e2e

import (
	"strings"
	"testing"
	"time"
)

// TestE2E_OfflineSweepAndRedelivery 验证：断连→offline→重启→online 与离线任务补投。
func TestE2E_OfflineSweepAndRedelivery(t *testing.T) {
	h := New(t)
	h.StartAgent("rel1", nil)
	h.waitAgentOnline("rel1")

	// 强杀 agent：应在 heartbeat_timeout(5s)+扫描周期内变 offline
	h.KillAgent("rel1")
	h.WaitFor(25*time.Second, func() bool {
		return strings.Contains(h.Ctl("agent", "list"), "offline")
	}, "agent 被标记 offline")

	// 离线期间创建任务（仍落 pending）
	out := h.Ctl("job", "run", "--selector", "name=rel1", "--timeout", "30", "printf redelivered")
	jobID := jobIDsFromRun(out)[0]

	// 重启 agent，pending 被补投执行
	h.StartAgentProcess("rel1")
	h.waitAgentOnline("rel1")
	h.WaitFor(15*time.Second, func() bool {
		show := h.Ctl("job", "show", jobID)
		return strings.Contains(show, "succeeded") && strings.Contains(show, "redelivered")
	}, "离线任务补投并成功")
}
