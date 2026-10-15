package store

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

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
			log.New(os.Stdout, "\r\n", log.LstdFlags),
			logger.Config{
				SlowThreshold:             200 * time.Millisecond,
				LogLevel:                  logger.Warn,
				IgnoreRecordNotFoundError: true,
				Colorful:                  true,
			},
		),
	})
	if err != nil {
		return nil, fmt.Errorf("打开数据库失败 %s: %w", filepath.Clean(path), err)
	}
	if err := db.AutoMigrate(&Alarm{}, &AlarmRule{}, &Setting{}); err != nil {
		return nil, fmt.Errorf("迁移数据库失败: %w", err)
	}
	return db, nil
}

// RefreshActiveDurations 刷新所有仍处于触发中（active）告警的持续时间。
func RefreshActiveDurations(db *gorm.DB, now time.Time) error {
	var alarms []Alarm
	if err := db.Select("id", "occurred_at").Where("state = ?", AlarmStateActive).Find(&alarms).Error; err != nil {
		return err
	}
	for _, alarm := range alarms {
		ms := now.Sub(alarm.OccurredAt).Milliseconds()
		if ms < 0 {
			ms = 0
		}
		if err := db.Model(&Alarm{}).Where("id = ?", alarm.ID).Update("duration_ms", ms).Error; err != nil {
			return err
		}
	}
	return nil
}
