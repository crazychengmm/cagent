package controlservice

import (
	"context"
	"sync"

	pb "github.com/crazychengmm/cagent/proto/cagent/v1"
)

// streamSession 表示一条 agent 控制流及其串行发送器。
type streamSession struct {
	agentID string
	stream  pb.ControlService_ControlServer
	cancel  context.CancelFunc
	sendMu  sync.Mutex
}

func (s *streamSession) send(msg *pb.ServerMessage) error {
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	return s.stream.Send(msg)
}

// streamRegistry 维护在线 agentID → 当前控制流；同 agent 重连时新流替换旧流。
type streamRegistry struct {
	mu       sync.Mutex
	sessions map[string]*streamSession
}

func newStreamRegistry() *streamRegistry {
	return &streamRegistry{sessions: make(map[string]*streamSession)}
}

// register 写入当前会话，返回被替换的旧会话（可能为 nil）。
func (r *streamRegistry) register(s *streamSession) *streamSession {
	r.mu.Lock()
	defer r.mu.Unlock()
	old := r.sessions[s.agentID]
	r.sessions[s.agentID] = s
	return old
}

// remove 仅当传入会话仍是当前会话时才删除，避免新连接被旧连接注销。
func (r *streamRegistry) remove(agentID string, s *streamSession) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if current, ok := r.sessions[agentID]; ok && current == s {
		delete(r.sessions, agentID)
	}
}

func (r *streamRegistry) get(agentID string) (*streamSession, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[agentID]
	return s, ok
}

// onlineAgentIDs 返回当前有活跃控制流的 agent 列表（快照副本）。
func (r *streamRegistry) onlineAgentIDs() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	ids := make([]string, 0, len(r.sessions))
	for id := range r.sessions {
		ids = append(ids, id)
	}
	return ids
}
