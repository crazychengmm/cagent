package jobworker

import (
	"sync"

	pb "github.com/crazychengmm/cagent/proto/cagent/v1"
)

// defaultResultCacheCapacity 是本地保留终态结果的任务数上限。
const defaultResultCacheCapacity = 1000

// resultCache 保存最近完成任务的终态，用于重发时直接回传、不重复执行（FIFO 淘汰）。
type resultCache struct {
	mu       sync.Mutex
	capacity int
	items    map[string]*pb.JobFinished
	order    []string
}

func newResultCache(capacity int) *resultCache {
	if capacity <= 0 {
		capacity = defaultResultCacheCapacity
	}
	return &resultCache{capacity: capacity, items: make(map[string]*pb.JobFinished)}
}

func (c *resultCache) put(jobID string, fin *pb.JobFinished) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.items[jobID]; !exists {
		c.order = append(c.order, jobID)
	}
	c.items[jobID] = fin
	for len(c.order) > c.capacity {
		oldest := c.order[0]
		c.order = c.order[1:]
		delete(c.items, oldest)
	}
}

func (c *resultCache) get(jobID string) (*pb.JobFinished, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	fin, ok := c.items[jobID]
	return fin, ok
}
