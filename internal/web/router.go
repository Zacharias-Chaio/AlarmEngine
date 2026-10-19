package web

import (
	"embed"
	"io/fs"
	"log/slog"
	"net/http"
	"runtime/debug"
	"strconv"
	"time"

	"alarmengine/internal/api"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
)

//go:embed all:static
var staticFS embed.FS

// NewRouter 组装路由：/api/* 走 REST，其他走静态单页。
func NewRouter(srv *api.Server) http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(accessLog)
	r.Use(recoverer)

	r.Route("/api", func(r chi.Router) {
		r.Get("/health", srv.Health)
		r.Get("/settings", srv.GetSettings)
		r.Post("/settings", srv.SaveSettings)
		r.Get("/system-info", srv.GetSystemInfo)
		r.Post("/restart", srv.Restart)

		r.Get("/alarms", srv.ListAlarms)
		r.Get("/alarms/summary", srv.AlarmSummary)
		r.Get("/alarms/{id}", srv.GetAlarm)
		r.Post("/alarms/{id}/acknowledge", srv.AcknowledgeAlarm)
		r.Post("/alarms/{id}/handle", srv.HandleAlarm)
		r.Post("/alarms/batch/acknowledge", srv.BatchAcknowledgeAlarms)
		r.Post("/alarms/batch/handle", srv.BatchHandleAlarms)

		r.Get("/history", srv.ListHistory)
		r.Get("/history/statistics", srv.HistoryStatistics)
		r.Get("/history/daily-distribution", srv.DailyDistribution)

		r.Get("/rules", srv.ListRules)
		r.Post("/rules", srv.SaveRule)
		r.Delete("/rules/{id}", srv.DeleteRule)

		r.Get("/definitions", srv.GetDefinitions)
		r.Post("/definitions/refresh", srv.RefreshDefinitions)
		r.Get("/actions", srv.ListActions)

		// 未匹配的 API 路径也返回统一 envelope（前端 apiReq 依赖 code 字段）。
		r.NotFound(func(w http.ResponseWriter, r *http.Request) {
			writeError(w, http.StatusNotFound, "接口不存在: "+r.URL.Path)
		})
		r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
			writeError(w, http.StatusMethodNotAllowed, "方法不允许: "+r.Method)
		})
	})

	staticSub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic("嵌入式静态资源路径错误: " + err.Error()) // 启动期失败必须立即暴露，不能静默空站点
	}
	fileServer := http.FileServer(http.FS(staticSub))
	// 静态资源禁止缓存：embed.FS 没有 ModTime，无法给出 ETag/Last-Modified，
	// 若允许缓存，升级后浏览器会长期运行旧 JS，与后端新契约混用。
	r.Handle("/*", noStore(fileServer))

	return r
}

// noStore 为静态资源加 Cache-Control: no-store（内嵌资源体积小，实时性优先）。
func noStore(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}

// writeError 以统一 envelope 返回错误（静态资源之外的兜底响应）。
func writeError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_, _ = w.Write([]byte(`{"code":` + strconv.Itoa(code) + `,"msg":"` + msg + `"}`))
}

func accessLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		started := time.Now()
		writer := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(writer, r)
		slog.Info("HTTP 请求完成",
			"method", r.Method,
			"path", r.URL.Path,
			"status", writer.Status(),
			"bytes", writer.BytesWritten(),
			"duration", time.Since(started),
			"request_id", middleware.GetReqID(r.Context()),
			"remote_addr", r.RemoteAddr,
		)
	})
}

func recoverer(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if recovered := recover(); recovered != nil {
				slog.Error("HTTP 请求发生 panic",
					"error", recovered,
					"request_id", middleware.GetReqID(r.Context()),
					"stack", string(debug.Stack()),
				)
				http.Error(w, http.StatusText(http.StatusInternalServerError), http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}
