package web

import (
	"embed"
	"io/fs"
	"log/slog"
	"net/http"
	"runtime/debug"
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
	})

	staticSub, _ := fs.Sub(staticFS, "static")
	fileServer := http.FileServer(http.FS(staticSub))
	r.Handle("/*", fileServer)

	return r
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
