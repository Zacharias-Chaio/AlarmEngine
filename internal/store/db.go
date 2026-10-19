package store

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// slogWriter 将 GORM 日志转发到 slog：慢查询/错误进入文件日志，终端不再混入 ANSI 转义。
type slogWriter struct{}

func (slogWriter) Printf(format string, args ...any) {
	slog.Warn(fmt.Sprintf(format, args...), "module", "store")
}

// Open opens the embedded SQLite database and applies schema migrations.
func Open(path string) (*gorm.DB, error) {
	// 写入性能 PRAGMA（经 DSN 下发，连接池内每个连接生效）：
	//   journal_mode=WAL   —— 读写不互斥，摄取写入不再被查询/刷新阻塞排队；
	//   synchronous=NORMAL —— WAL 模式下安全，避免每次提交强制刷盘放大磁盘尾延迟；
	//   busy_timeout=5000  —— 写锁竞争时等待重试，而非立即返回 SQLITE_BUSY。
	// 注意：路径包含 '?' 或 '&' 时需先编码（与 mattn/go-sqlite3 DSN 约定一致）。
	dsn := path + "?_pragma=busy_timeout(5000)&_pragma=journal_mode(WAL)&_pragma=synchronous(NORMAL)"
	db, err := gorm.Open(sqlite.Open(dsn), &gorm.Config{
		// 与 logger.Default 一致，但忽略 record not found：
		// 定位键探测（合并/恢复）把"查无活动告警"当正常分支，不应产生 Warn 噪音。
		Logger: logger.New(
			slogWriter{},
			logger.Config{
				SlowThreshold:             200 * time.Millisecond,
				LogLevel:                  logger.Warn,
				IgnoreRecordNotFoundError: true,
				Colorful:                  false,
			},
		),
	})
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败 %s: %w", filepath.Clean(path), err)
	}
	if err := db.AutoMigrate(&Alarm{}, &AlarmRule{}, &ActionRecord{}, &Setting{}); err != nil {
		return nil, fmt.Errorf("迁移数据库失败: %w", err)
	}
	migrateLegacyRules(db)
	return db, nil
}

// migrateLegacyRules 旧单源规则（上一版本 property_key/operator/threshold 模型）处理。
// 旧表没有 event_key/service_key 列，无法可靠合成新模型的 event 监测源/控制源——
// 强行合成只会得到"永不命中或误命中"的伪规则。因此这里只停用并告警，等待人工按新模型重建；
// 另做一次完整性修复：历史错误迁移产物（event 源缺 eventKey / 控制源缺 serviceKey）同样停用。
func migrateLegacyRules(db *gorm.DB) {
	var legacy []AlarmRule
	if err := db.Where("(monitors IS NULL OR monitors IN ('', '[]', 'null')) AND device_id != ''").Find(&legacy).Error; err != nil {
		slog.Warn("旧规则迁移查询失败，跳过迁移", "error", err)
		return
	}
	for _, rule := range legacy {
		if err := db.Model(&AlarmRule{}).Where("id = ?", rule.ID).Update("enabled", false).Error; err != nil {
			slog.Warn("旧规则停用失败", "id", rule.ID, "error", err)
			continue
		}
		slog.Warn("旧单源规则已停用，请按多监测源模型人工重建",
			"id", rule.ID, "name", rule.Name,
			"device_id", rule.DeviceID, "target_device_id", rule.TargetDeviceID, "command_value", rule.CommandValue)
	}
	repairBrokenRules(db)
}

// repairBrokenRules 停用"不可能正确工作"的规则：
// event 监测源缺 deviceId/eventKey（永不命中），或设备控制源缺 deviceId/serviceKey（无法下发）。
// SaveRule 已强制这些字段非空，因此正常规则不会被误伤。
func repairBrokenRules(db *gorm.DB) {
	var rules []AlarmRule
	if err := db.Find(&rules).Error; err != nil {
		slog.Warn("规则完整性检查失败", "error", err)
		return
	}
	for _, rule := range rules {
		if !rule.Enabled {
			continue
		}
		broken := false
		for _, monitor := range rule.Monitors {
			if monitor.Kind == "event" && (monitor.DeviceID == "" || monitor.EventKey == "") {
				broken = true
			}
		}
		for _, control := range rule.Controls {
			if control.Kind == "device" && (control.DeviceID == "" || control.ServiceKey == "") {
				broken = true
			}
		}
		if !broken {
			continue
		}
		if err := db.Model(&AlarmRule{}).Where("id = ?", rule.ID).Update("enabled", false).Error; err != nil {
			slog.Warn("停用不完整规则失败", "id", rule.ID, "error", err)
			continue
		}
		slog.Warn("规则监测源/控制源不完整（疑似旧版本迁移产物），已停用，请人工重建", "id", rule.ID, "name", rule.Name)
	}
}

// 说明：告警持续时间改为 API 读取时派生（api.deriveDuration），不再周期落库刷新。
