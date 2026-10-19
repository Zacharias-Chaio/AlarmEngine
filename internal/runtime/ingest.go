package runtime

import (
	"sync"
	"sync/atomic"
	"time"
)

// ingestState 告警摄取的内存态：alarm_id 幂等去重缓存 + 最近摄取时间。
type ingestState struct {
	seenMu    sync.Mutex
	seen      map[string]struct{}
	seenOrder []string
	lastMs    atomic.Int64
}

func newIngestState() ingestState {
	return ingestState{seen: make(map[string]struct{})}
}

// markSeen 幂等去重：返回 false 表示重复消息应丢弃。
func (s *ingestState) markSeen(alarmID string) bool {
	if alarmID == "" {
		return true
	}
	s.seenMu.Lock()
	defer s.seenMu.Unlock()
	if _, dup := s.seen[alarmID]; dup {
		return false
	}
	s.seen[alarmID] = struct{}{}
	s.seenOrder = append(s.seenOrder, alarmID)
	if len(s.seenOrder) > seenCapacity {
		evict := s.seenOrder[0]
		s.seenOrder = s.seenOrder[1:]
		delete(s.seen, evict)
	}
	return true
}

// touch 记录最近一次成功校验/去重通过的消息时间。
func (s *ingestState) touch() { s.lastMs.Store(time.Now().UnixMilli()) }

// lastIngestAt 返回最近摄取时间（零值表示尚未收到消息）。
func (s *ingestState) lastIngestAt() time.Time {
	if ms := s.lastMs.Load(); ms > 0 {
		return time.UnixMilli(ms).UTC()
	}
	return time.Time{}
}
