package store

import (
	"fmt"
	"path/filepath"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

// Open opens the embedded SQLite database and applies schema migrations.
func Open(path string) (*gorm.DB, error) {
	db, err := gorm.Open(sqlite.Open(path), &gorm.Config{})
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
