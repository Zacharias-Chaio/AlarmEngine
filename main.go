package main

import (
	"context"
	"flag"
	"fmt"
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
	if err := run(); err != nil {
		slog.Error("服务异常退出", "error", err)
		os.Exit(1) // run() 已返回：其内部 defer 均已执行（停运行时、关库、刷日志）
	}
}

// run 承载服务生命周期；错误通过返回值上抛，保证关闭路径上的 defer 一定执行。
func run() error {
	addr := flag.String("addr", ":8091", "HTTP 监听地址")
	dbPath := flag.String("db", "data/alarmengine.db", "SQLite 数据库文件（相对或绝对路径）")
	flag.Parse()

	absDBPath, err := filepath.Abs(*dbPath)
	if err != nil {
		return fmt.Errorf("解析数据库路径失败: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(absDBPath), 0755); err != nil {
		return fmt.Errorf("创建数据库目录失败: %w", err)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	db, err := store.Open(absDBPath)
	if err != nil {
		return fmt.Errorf("初始化数据库失败: %w", err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		return fmt.Errorf("获取数据库连接失败: %w", err)
	}
	defer sqlDB.Close()

	settings, err := store.OpenSettings(db)
	if err != nil {
		return fmt.Errorf("加载平台配置失败: %w", err)
	}
	logRuntime, err := logging.NewRuntime(logging.Config{Logger: settings.Get().Logger})
	if err != nil {
		return fmt.Errorf("初始化日志失败: %w", err)
	}
	defer logRuntime.Close()

	rt, err := runtime.NewManager(ctx, settings.Get(), db)
	if err != nil {
		return fmt.Errorf("初始化告警运行时失败: %w", err)
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

	serverErr := make(chan error, 1)
	go func() {
		slog.Info("告警引擎服务启动", "address", "http://localhost"+*addr)
		if err := httpSrv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			serverErr <- err // 交给主协程走正常关闭路径
		}
	}()

	var shutdownErr error
	select {
	case err := <-serverErr:
		shutdownErr = err
	case <-ctx.Done():
		slog.Info("收到退出信号，正在关闭服务")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpSrv.Shutdown(shutdownCtx); err != nil {
		slog.Warn("强制关闭", "error", err)
	}
	slog.Info("服务已停止")
	if shutdownErr != nil {
		return fmt.Errorf("HTTP 服务异常: %w", shutdownErr)
	}
	return nil
}
