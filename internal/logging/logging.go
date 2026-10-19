// Package logging configures application logging from platform settings.
package logging

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/natefinch/lumberjack.v2"
)

// Config describes the logger section of the platform settings.
type Config struct {
	Logger LoggerConfig `json:"logger"`
}

// LoggerConfig controls log level and destinations.
type LoggerConfig struct {
	Level      string     `json:"level"`
	Console    bool       `json:"console"`
	File       bool       `json:"file"`
	FileConfig FileConfig `json:"fileConfig"`
}

// FileConfig controls rotating file output. Size is measured in megabytes.
type FileConfig struct {
	Filename   string `json:"filename"`
	MaxSize    int    `json:"maxSize"`
	MaxBackups int    `json:"maxBackups"`
	MaxAge     int    `json:"maxAge"`
	Compress   bool   `json:"compress"`
}

// DefaultConfig returns the built-in logging configuration.
func DefaultConfig() Config {
	return Config{Logger: LoggerConfig{
		Level:   "info",
		Console: true,
		File:    true,
		FileConfig: FileConfig{
			Filename:   "logs/alarmengine.log",
			MaxSize:    10,
			MaxBackups: 5,
			MaxAge:     30,
			Compress:   true,
		},
	}}
}

// Validate checks that the logging configuration is usable.
func (config Config) Validate() error {
	if _, err := parseLevel(config.Logger.Level); err != nil {
		return err
	}
	if !config.Logger.Console && !config.Logger.File {
		return errors.New("日志配置至少要启用终端或文件输出")
	}
	if !config.Logger.File {
		return nil
	}
	fileConfig := config.Logger.FileConfig
	if strings.TrimSpace(fileConfig.Filename) == "" {
		return errors.New("文件日志已启用但 filename 为空")
	}
	if fileConfig.MaxSize <= 0 {
		return errors.New("文件日志 maxSize 必须大于 0")
	}
	if fileConfig.MaxBackups < 0 || fileConfig.MaxAge < 0 {
		return errors.New("文件日志 maxBackups 和 maxAge 不能小于 0")
	}
	return nil
}

func newHandler(config Config) (slog.Handler, io.Closer, error) {
	if err := config.Validate(); err != nil {
		return nil, nil, err
	}
	level, _ := parseLevel(config.Logger.Level)
	writers := make([]io.Writer, 0, 2)
	var fileWriter *lumberjack.Logger
	if config.Logger.Console {
		writers = append(writers, os.Stdout)
	}
	if config.Logger.File {
		fileConfig := config.Logger.FileConfig
		if err := os.MkdirAll(filepath.Dir(fileConfig.Filename), 0755); err != nil {
			return nil, nil, fmt.Errorf("创建日志目录: %w", err)
		}
		fileWriter = &lumberjack.Logger{
			Filename:   fileConfig.Filename,
			MaxSize:    fileConfig.MaxSize,
			MaxBackups: fileConfig.MaxBackups,
			MaxAge:     fileConfig.MaxAge,
			Compress:   fileConfig.Compress,
		}
		writers = append(writers, fileWriter)
	}
	return slog.NewTextHandler(io.MultiWriter(writers...), &slog.HandlerOptions{Level: level}), fileWriter, nil
}

func parseLevel(value string) (slog.Level, error) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(strings.ToUpper(strings.TrimSpace(value)))); err != nil {
		return 0, fmt.Errorf("无效日志级别 %q，应为 debug、info、warn 或 error", value)
	}
	return level, nil
}
