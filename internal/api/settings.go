package api

import (
	"encoding/json"
	"net/http"
	"runtime"
	"time"

	"alarmengine/internal/buildinfo"
	"alarmengine/internal/config"
	"alarmengine/internal/logging"
)

// GetSettings returns the editable application settings persisted in the database.
func (s *Server) GetSettings(w http.ResponseWriter, r *http.Request) {
	if s.Settings == nil {
		fail(w, http.StatusServiceUnavailable, "平台设置未就绪")
		return
	}
	ok(w, s.Settings.Get())
}

// SettingsSaveResult 设置保存结果：明确"已保存"与"运行时是否已生效"。
// 消息总线重连失败时配置已落库且已进入自动退避重试，不能回滚告诉用户"没保存"。
type SettingsSaveResult struct {
	Settings      config.App `json:"settings"`
	RuntimeReload string     `json:"runtimeReload"` // ok | pending（等待自动重试）
	Message       string     `json:"message,omitempty"`
}

// SaveSettings persists settings, then hot-reloads logging and the NATS clients.
func (s *Server) SaveSettings(w http.ResponseWriter, r *http.Request) {
	if s.Settings == nil {
		fail(w, http.StatusServiceUnavailable, "平台设置未就绪")
		return
	}
	var settings config.App
	if err := json.NewDecoder(r.Body).Decode(&settings); err != nil {
		fail(w, http.StatusBadRequest, "JSON 解析失败: "+err.Error())
		return
	}
	if err := s.Settings.Save(settings); err != nil {
		fail(w, http.StatusBadRequest, "保存平台设置失败: "+err.Error())
		return
	}
	if s.Logger != nil {
		if err := s.Logger.Apply(logging.Config{Logger: settings.Logger}); err != nil {
			internalError(w, err)
			return
		}
	}
	result := SettingsSaveResult{Settings: settings, RuntimeReload: "ok"}
	if s.Runtime != nil {
		if err := s.Runtime.ReloadBus(settings); err != nil {
			// 已保存 + 已排定退避重试：如实告知"待生效"，而不是让用户以为没保存。
			result.RuntimeReload = "pending"
			result.Message = "设置已保存；消息总线重连失败，已进入自动重试：" + err.Error()
		}
	}
	ok(w, result)
}

// SystemInfo 软件信息。
type SystemInfo struct {
	OperatingSystem    string `json:"operatingSystem"`
	SystemTime         string `json:"systemTime"`
	AlarmEngineVersion string `json:"alarmEngineVersion"`
}

func (s *Server) GetSystemInfo(w http.ResponseWriter, r *http.Request) {
	ok(w, SystemInfo{
		OperatingSystem:    runtime.GOOS,
		SystemTime:         time.Now().UTC().Format(time.RFC3339),
		AlarmEngineVersion: buildinfo.Version,
	})
}

// Restart restarts NATS clients in process while the HTTP server remains available.
func (s *Server) Restart(w http.ResponseWriter, r *http.Request) {
	if s.Settings == nil {
		fail(w, http.StatusServiceUnavailable, "平台设置未就绪")
		return
	}
	if s.Runtime == nil {
		fail(w, http.StatusServiceUnavailable, "运行时重启不可用")
		return
	}
	if !s.Runtime.Restart(s.Settings.Get()) {
		fail(w, http.StatusConflict, "软件正在重启，请稍候")
		return
	}
	okStatus(w, http.StatusAccepted, map[string]string{"status": "restarting"})
}

// Health 健康检查：数据库连通性 + 运行时状态（NATS 连接、定义同步、最近摄取），
// 供部署探针与运维发现"跑着但没在收消息"的静默故障。
func (s *Server) Health(w http.ResponseWriter, r *http.Request) {
	status := map[string]any{"time": time.Now().UTC(), "db": "ok"}
	if sqlDB, err := s.DB.DB(); err != nil || sqlDB.Ping() != nil {
		status["db"] = "error"
	}
	if s.Runtime != nil {
		status["runtime"] = s.Runtime.Status()
	}
	ok(w, status)
}
