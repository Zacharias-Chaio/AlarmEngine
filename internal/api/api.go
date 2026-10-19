package api

import (
	"encoding/json"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"alarmengine/internal/config"
	"alarmengine/internal/logging"
	"alarmengine/internal/runtime"
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
// P2：合并定义缓存能力为单一注入接口——此前 definitions 经运行时类型断言获取，
// facade 名存实亡；现承认对 runtime 的编译期依赖（单体二进制），接口仍是测试桩的接缝。
type RuntimeFacade interface {
	ReloadBus(config.App) error
	Restart(config.App) bool
	DefinitionsSnapshot() runtime.DefinitionsSnapshot
	RefreshDefinitions() error
	Status() runtime.Status
	ResetRuleState(ruleID string)
	InvalidateRules()
}

// ok 统一成功响应：{"code":0,"data":<any>}
func ok(w http.ResponseWriter, data any) {
	okStatus(w, http.StatusOK, data)
}

// okStatus 指定状态码的成功响应（202 异步受理等），避免各处手写 envelope。
func okStatus(w http.ResponseWriter, code int, data any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": 0, "data": data})
}

// fail 统一失败响应：{"code":<http>,"msg":"..."}
func fail(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"code": code, "msg": msg})
}

// deriveDuration 读取时派生告警持续时间（不再周期落库刷新）：
// 活动告警 = 当前时间 - 发生时间；已恢复 = 恢复时间 - 发生时间；结果钳制非负。
func deriveDuration(alarm *store.Alarm, now time.Time) {
	end := now
	if alarm.State != store.AlarmStateActive && alarm.RecoveredAt != nil {
		end = *alarm.RecoveredAt
	}
	alarm.DurationMs = end.Sub(alarm.OccurredAt).Milliseconds()
	if alarm.DurationMs < 0 {
		alarm.DurationMs = 0
	}
}

// internalError 记录内部错误并返回通用文案：不向客户端泄露 SQL/表名/路径等细节。
func internalError(w http.ResponseWriter, err error) {
	slog.Error("请求处理内部错误", "error", err)
	fail(w, http.StatusInternalServerError, "内部错误，请查看服务日志")
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
