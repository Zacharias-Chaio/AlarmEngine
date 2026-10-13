package api

import (
	"encoding/json"
	"net/http"
	"strconv"

	"alarmengine/internal/config"
	"alarmengine/internal/logging"
	"alarmengine/internal/store"

	"gorm.io/gorm"
)

// Server REST 处理器容器，依赖注入存储、运行时与日志。
type Server struct {
	DB       *gorm.DB
	Settings *store.SettingsStore
	Runtime  RuntimeFacade
	Logger   *logging.Runtime
}

// RuntimeFacade 保持 API 处理器与运行时实现解耦。
type RuntimeFacade interface {
	ReloadBus(config.App) error
	Restart(config.App) bool
}

// ok 统一成功响应：{"code":0,"data":<any>}
func ok(w http.ResponseWriter, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data})
}

// fail 统一失败响应：{"code":<http>,"msg":"..."}
func fail(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "msg": msg})
}

// Paged 分页列表统一响应。
type Paged struct {
	List     any   `json:"list"`
	Total    int64 `json:"total"`
	Page     int   `json:"page"`
	PageSize int   `json:"pageSize"`
}

// 列表分页默认值与上限。
const (
	defaultPageSize = 15
	maxPageSize     = 100
)

// paging 解析分页参数：page 从 1 起，pageSize 默认 15、上限 100。
func paging(r *http.Request) (page, pageSize int) {
	page, _ = strconv.Atoi(r.URL.Query().Get("page"))
	if page < 1 {
		page = 1
	}
	pageSize, _ = strconv.Atoi(r.URL.Query().Get("pageSize"))
	if pageSize < 1 {
		pageSize = defaultPageSize
	}
	if pageSize > maxPageSize {
		pageSize = maxPageSize
	}
	return page, pageSize
}
