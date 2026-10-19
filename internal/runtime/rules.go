package runtime

import (
	"sync"
	"time"

	"alarmengine/internal/store"
)

// ruleCache 启用规则内存缓存：每条告警消息都全表查规则会放大 SQLite 压力。
// 规则保存/删除后由 API 侧调用 InvalidateRules / ResetRuleState 失效（保证"保存即生效"）。
type ruleCache struct {
	mu    sync.RWMutex
	rules []store.AlarmRule
	ready bool
}

func (c *ruleCache) snapshot() ([]store.AlarmRule, bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.rules, c.ready
}

func (c *ruleCache) set(rules []store.AlarmRule) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rules = rules
	c.ready = true
}

func (c *ruleCache) invalidate() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.rules = nil
	c.ready = false
}

// ruleEngine 规则评估与保护动作的内存态（评估逻辑仍在 engine.go，状态集中于此）。
type ruleEngine struct {
	ruleCache ruleCache

	// 规则成立状态（上升沿检测，内存态；重启丢失后由 seedRuleHoldState 重建）。
	ruleHoldMu    sync.Mutex
	ruleHoldState map[string]bool

	// 同规则下发冷却（内存态）。
	cooldownMu   sync.Mutex
	lastDispatch map[string]time.Time

	// 控制下发按设备串行化：同一设备的冲突指令必须保持确定顺序（规则并发执行时会乱序）。
	deviceMu    sync.Mutex
	deviceLocks map[string]*sync.Mutex
}
