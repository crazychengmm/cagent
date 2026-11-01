package management

import (
	"sync"

	"github.com/crazychengmm/cagent/internal/server/controlservice"
	pb "github.com/crazychengmm/cagent/proto/cagent/v1"
)

// recordingOnline 同时记录升级指令与取消指令。
type recordingOnline struct {
	mu      sync.Mutex
	ids     []string
	cancels []string
}

func (r *recordingOnline) OnlineAgentIDs() []string { return r.ids }

func (r *recordingOnline) SendToAgent(agentID string, msg *pb.ServerMessage) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	online := false
	for _, id := range r.ids {
		if id == agentID {
			online = true
		}
	}
	if !online {
		return controlservice.ErrAgentOffline
	}
	if c := msg.GetJobCancel(); c != nil {
		r.cancels = append(r.cancels, c.GetJobId())
	}
	return nil
}

func (r *recordingOnline) cancelFor(agentID string) string {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.cancels) == 0 {
		return ""
	}
	return r.cancels[0]
}
