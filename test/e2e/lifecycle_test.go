package e2e

import (
	"strings"
	"testing"
	"time"
)

func TestE2E_RegistrationAndOnline(t *testing.T) {
	h := New(t)
	h.StartAgent("a1", map[string]string{"env": "prod", "role": "db"})

	h.WaitFor(15*time.Second, func() bool {
		out := h.Ctl("agent", "list")
		return strings.Contains(out, "online") &&
			strings.Contains(out, "name=a1") &&
			strings.Contains(out, "env=prod")
	}, "agent 注册并上线")
}
