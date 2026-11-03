package store

import (
	"path/filepath"
	"testing"
)

// 真实文件库：建表、迁移与重复打开幂等。
func TestOpenRealFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app db.db") // 含空格路径
	db, err := Open(path)
	if err != nil {
		t.Fatalf("打开数据库失败: %v", err)
	}
	var count int64
	if err := db.Model(&AlarmRule{}).Count(&count).Error; err != nil {
		t.Fatalf("规则表不可用: %v", err)
	}
	if sqlDB, err := db.DB(); err == nil {
		_ = sqlDB.Close()
	}
	// 二次打开：迁移幂等，不应报错。
	db2, err := Open(path)
	if err != nil {
		t.Fatalf("二次打开数据库失败: %v", err)
	}
	if sqlDB, err := db2.DB(); err == nil {
		_ = sqlDB.Close()
	}
}
