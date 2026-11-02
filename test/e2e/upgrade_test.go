package e2e

import (
	"strings"
	"testing"
	"time"
)

// TestE2E_SelfUpgrade 验证：发布新版本→upgrade 下发→验签替换→guard 健康门控→
// 新版本上线（agent list 显示新版本号）。
func TestE2E_SelfUpgrade(t *testing.T) {
	h := New(t)
	h.StartAgent("up1", nil)
	defer func() {
		if t.Failed() {
			t.Logf("==== server log ====\n%s", h.ServerLog())
			h.DumpAgent("up1")
		}
	}()
	h.waitAgentOnline("up1")

	// 初始版本
	h.WaitFor(5*time.Second, func() bool {
		return strings.Contains(h.Ctl("agent", "list"), "1.0.0")
	}, "初始版本 1.0.0")

	// 构建并发布 999.0.0
	v2 := h.BuildVersionedBinary("cagent-999", "999.0.0")
	h.PublishRelease("999.0.0", v2)

	// 发起升级
	out := h.Ctl("upgrade", "--release", "999.0.0", "--selector", "name=up1")
	if !strings.Contains(out, "1 台") {
		t.Fatalf("升级响应异常: %s", out)
	}

	// 新版本经下载验签→grace→替换→重启→健康门控后上线
	h.WaitFor(40*time.Second, func() bool {
		return strings.Contains(h.Ctl("agent", "list"), "999.0.0") &&
			strings.Contains(h.Ctl("agent", "list"), "online")
	}, "升级到 999.0.0 并上线")

	// upgrade job 终态成功
	jobs := h.Ctl("job", "list", "--status", "succeeded")
	if !strings.Contains(jobs, "upgrade") {
		t.Errorf("升级 job 未成功:\n%s", jobs)
	}
}

// TestE2E_DowngradeRejectedByDefault 验证默认拒绝降级。
func TestE2E_DowngradeRejectedByDefault(t *testing.T) {
	h := New(t)
	h.StartAgent("up2", nil)
	h.waitAgentOnline("up2")

	// 发布比 1.0.0 更低的 0.9.0
	v09 := h.BuildVersionedBinary("cagent-090", "0.9.0")
	h.PublishRelease("0.9.0", v09)

	h.Ctl("upgrade", "--release", "0.9.0", "--selector", "name=up2")

	// agent 应拒绝降级，upgrade job 最终 failed，agent 仍是 1.0.0
	h.WaitFor(15*time.Second, func() bool {
		return strings.Contains(h.Ctl("job", "list", "--status", "failed"), "upgrade")
	}, "降级 upgrade job failed")
}
