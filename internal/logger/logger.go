// Package logger 封装 zap，提供全局 logger 与按服务区分的命名空间。
package logger

import (
	"context"
	"fmt"
	"os"
	"path/filepath"

	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"gopkg.in/natefinch/lumberjack.v2"
)

// L 是全局 logger，在 Init 后即可使用。Init 前为 nil。
var L *zap.Logger

// Init 初始化全局 logger。service 用作日志字段 service 区分来源（api/transfer）。
// level: debug/info/warn/error；logDir 为空时仅输出到 stdout。
func Init(service, level, logDir string) error {
	var lvl zapcore.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		return fmt.Errorf("invalid log level %q: %w", level, err)
	}

	encoderCfg := zapcore.EncoderConfig{
		TimeKey:        "ts",
		LevelKey:       "level",
		NameKey:        "logger",
		CallerKey:      "caller",
		FunctionKey:    zapcore.OmitKey,
		MessageKey:     "msg",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.CapitalLevelEncoder,
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeDuration: zapcore.StringDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}

	consoleEncoder := zapcore.NewConsoleEncoder(encoderCfg)
	consoleSyncer := zapcore.AddSync(os.Stdout)

	cores := []zapcore.Core{
		zapcore.NewCore(consoleEncoder, consoleSyncer, lvl),
	}

	if logDir != "" {
		if err := os.MkdirAll(logDir, 0o755); err != nil {
			return fmt.Errorf("create log dir: %w", err)
		}
		roller := &lumberjack.Logger{
			Filename:   filepath.Join(logDir, service+".log"),
			MaxSize:    100, // MB
			MaxBackups: 7,
			MaxAge:     30, // days
			Compress:   true,
		}
		fileEncoder := zapcore.NewJSONEncoder(encoderCfg)
		cores = append(cores, zapcore.NewCore(fileEncoder, zapcore.AddSync(roller), lvl))
	}

	L = zap.New(
		zapcore.NewTee(cores...),
		zap.AddCaller(),
		zap.Fields(zap.String("service", service)),
	)
	return nil
}

// Sync 在进程退出前调用，刷新缓冲。
func Sync() {
	if L != nil {
		_ = L.Sync()
	}
}

// FromContext 返回带 trace_id/span_id 字段的 logger。
// 从 ctx 的 active span 提取 SpanContext，注入为 zap 字段，
// 使每条日志都能关联到所属 trace。无 active span 时返回全局 L（无 trace 字段）。
// 中间件（GinLogger/HertzLogger）和业务 handler 应优先用 FromContext(ctx) 替代直接用 L。
func FromContext(ctx context.Context) *zap.Logger {
	if L == nil || ctx == nil {
		return L
	}
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() {
		return L
	}
	return L.With(
		zap.String("trace_id", sc.TraceID().String()),
		zap.String("span_id", sc.SpanID().String()),
	)
}
