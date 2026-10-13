package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"alarmengine/internal/api"
	"alarmengine/internal/logging"
	"alarmengine/internal/runtime"
	"alarmengine/internal/store"
	"alarmengine/internal/web"
)

func main() {
	addr := flag.String("addr", ":8091", "HTTP 监听地址")
	dbPath := flag.String("db", "data/alarmengine.db", "SQLite 数据库文件（相对或绝对路径）")
	flag.Parse()

	absDBPath, err := filepath.Abs(*dbPath)
	if err != nil {
		fatal("解析数据库路径失败", err)
	}
	if err := os.MkdirAll(filepath.Dir(absDBPath), 0755); err != nil {
		fatal("创建数据库目录失败", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	db, err := store.Open(absDBPath)
	if err != nil {
		fatal("初始化数据库失败", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		fatal("获取数据库连接失败", err)
	}
	defer sqlDB.Close()

	settings, err := store.OpenSettings(db)
	if err != nil {
		fatal("加载平台配置失败", err)
	}
	logRuntime, err := logging.NewRuntime(logging.Config{Logger: settings.Get().Logger})
	if err != nil {
		fatal("初始化日志失败", err)
	}
	defer logRuntime.Close()

	// 首次启动写入演示数据，便于直接查看 Web 效果。
	if err := store.SeedIfEmpty(db); err != nil {
		slog.Warn("写入演示数据失败", "error", err)
	}

	rt, err := runtime.NewManager(ctx, settings.Get(), db)
	if err != nil {
		fatal("初始化告警运行时失败", err)
	}
	defer rt.Stop()

	srv := &api.Server{DB: db, Settings: settings, Runtime: rt, Logger: logRuntime}
	router := web.NewRouter(srv)

	httpSrv := &http.Server{
		Addr:         *addr,
		Handler:      router,
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	go func() {
		slog.Info("告警引擎服务启动", "address", "http://localhost"+*addr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			fatal("HTTP 服务异常", err)
		}
	}()

	<-ctx.Done()
	slog.Info("收到退出信号，正在关闭服务")

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(ctx); err != nil {
		slog.Warn("强制关闭", "error", err)
	}
	slog.Info("服务已停止")
}

func fatal(message string, err error) {
	slog.Error(message, "error", err)
	os.Exit(1)
}
