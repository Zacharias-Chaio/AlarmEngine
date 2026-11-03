package store

import (
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func openTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("打开内存数据库失败: %v", err)
	}
	if err := db.AutoMigrate(&Alarm{}); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	if sqlDB, err := db.DB(); err == nil {
		sqlDB.SetMaxOpenConns(1) // 内存 SQLite 每连接独立库
	}
	return db
}

func TestJuliandayMillisecondMath(t *testing.T) {
	// 验证统计 SQL 的 julianday 毫秒差值与存储格式（UTC 文本）兼容。
	db := openTestDB(t)
	start := time.Now().UTC().Add(-5 * time.Minute)
	end := time.Now().UTC()
	db.Create(&Alarm{ID: "a1", State: AlarmStateCleared, OccurredAt: start, Acknowledged: true, AcknowledgedAt: &end})

	var ms int64
	if err := db.Raw(
		"SELECT CAST((julianday(acknowledged_at) - julianday(occurred_at)) * 86400000 AS INTEGER) FROM alarms WHERE id = 'a1'",
	).Scan(&ms).Error; err != nil {
		t.Fatalf("查询失败: %v", err)
	}
	expect := 5 * time.Minute.Milliseconds()
	const tolerance = int64(2000) // 毫秒
	if ms < expect-tolerance || ms > expect+tolerance {
		t.Fatalf("julianday 毫秒差值错误: got %d want≈%d", ms, expect)
	}
}

func TestMigrateLegacyRules(t *testing.T) {
	db := openTestDB(t)
	if err := db.AutoMigrate(&AlarmRule{}); err != nil {
		t.Fatalf("迁移规则表失败: %v", err)
	}
	// 旧单源规则（monitors 为空）：旧表无 event_key/service_key 列，无法可靠合成 → 应停用且不伪造监测源。
	// 注意用 map 创建（Model+map）：结构体 Create 会被 default tag 改写 enabled。
	db.Model(&AlarmRule{}).Create(map[string]any{
		"id": "r-old", "name": "旧规则", "enabled": true, "device_id": "bms-001",
		"target_device_id": "pcs-001", "command_value": 80, "monitors": "[]", "controls": "[]",
	})
	// 新模型规则（已有 monitors）不应被改写。
	db.Create(&AlarmRule{
		ID: "r-new", Name: "新规则", Enabled: true,
		Monitors: RuleMonitors{{Kind: "level", Levels: []int{3}}},
		Controls: RuleControls{{Kind: "device", DeviceID: "pcs-002", ServiceKey: "stop", Value: 1}},
	})
	// 历史错误迁移产物：event 源缺 eventKey（永不命中）→ 应被停用。
	db.Create(&AlarmRule{
		ID: "r-broken", Name: "错误迁移产物", Enabled: true,
		Monitors: RuleMonitors{{Kind: "event", DeviceID: "bms-001", EventKey: ""}},
		Controls: RuleControls{{Kind: "device", DeviceID: "pcs-001", ServiceKey: "", Value: 1}},
	})

	migrateLegacyRules(db)

	var old, fresh, broken AlarmRule
	db.First(&old, "id = ?", "r-old")
	db.First(&fresh, "id = ?", "r-new")
	db.First(&broken, "id = ?", "r-broken")

	if old.Enabled {
		t.Fatalf("旧规则应被停用（不可自动合成有效监测源）: %+v", old)
	}
	if len(old.Monitors) != 0 || len(old.Controls) != 0 {
		t.Fatalf("旧规则不应伪造监测源/控制源: %+v / %+v", old.Monitors, old.Controls)
	}
	if !fresh.Enabled {
		t.Fatalf("新模型规则不应被停用: %+v", fresh)
	}
	if broken.Enabled {
		t.Fatalf("缺 eventKey 的规则应被停用: %+v", broken)
	}

	// 幂等：重复迁移不产生副作用。
	migrateLegacyRules(db)
	var count int64
	db.Model(&AlarmRule{}).Where("id = ?", "r-old").Count(&count)
	if count != 1 {
		t.Fatalf("迁移应幂等")
	}
	var oldAgain AlarmRule
	db.First(&oldAgain, "id = ?", "r-old")
	if oldAgain.Enabled {
		t.Fatalf("重复迁移不应重新启用旧规则")
	}
}
