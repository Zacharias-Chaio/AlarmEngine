package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"alarmengine/internal/config"

	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

// Setting 是平台配置的单行持久化表，ConfigJSON 保存完整的 config.App 快照。
type Setting struct {
	ID         int       `gorm:"primaryKey" json:"-"`
	ConfigJSON []byte    `gorm:"not null" json:"-"`
	UpdatedAt  time.Time `json:"updatedAt"`
}

const settingsRowID = 1

// SettingsStore 以数据库为唯一配置源的平台设置存储。
type SettingsStore struct {
	db  *gorm.DB
	mu  sync.RWMutex
	app config.App
}

// OpenSettings 加载或初始化平台配置。首次启动持久化默认配置；已有记录缺失字段回落默认值。
func OpenSettings(db *gorm.DB) (*SettingsStore, error) {
	var record Setting
	err := db.Session(&gorm.Session{Logger: logger.Discard}).First(&record, "id = ?", settingsRowID).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		app := config.Default()
		raw, err := json.Marshal(app)
		if err != nil {
			return nil, fmt.Errorf("序列化默认配置失败: %w", err)
		}
		record = Setting{ID: settingsRowID, ConfigJSON: raw, UpdatedAt: time.Now().UTC()}
		if err := db.Create(&record).Error; err != nil {
			return nil, fmt.Errorf("写入默认配置失败: %w", err)
		}
		return &SettingsStore{db: db, app: app}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取平台配置失败: %w", err)
	}
	app := config.Default()
	if err := json.Unmarshal(record.ConfigJSON, &app); err != nil {
		return nil, fmt.Errorf("解析平台配置失败: %w", err)
	}
	return &SettingsStore{db: db, app: app}, nil
}

// Get 返回当前配置的深拷贝。
func (s *SettingsStore) Get() config.App {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return cloneApp(s.app)
}

// Save 校验并持久化配置到数据库，同时更新内存副本。
func (s *SettingsStore) Save(app config.App) error {
	if err := config.Validate(app); err != nil {
		return err
	}
	raw, err := json.Marshal(app)
	if err != nil {
		return fmt.Errorf("序列化平台配置失败: %w", err)
	}
	record := Setting{ID: settingsRowID, ConfigJSON: raw, UpdatedAt: time.Now().UTC()}
	if err := s.db.Save(&record).Error; err != nil {
		return fmt.Errorf("写入平台配置失败: %w", err)
	}
	s.mu.Lock()
	s.app = cloneApp(app)
	s.mu.Unlock()
	return nil
}

func cloneApp(app config.App) config.App {
	return app
}
