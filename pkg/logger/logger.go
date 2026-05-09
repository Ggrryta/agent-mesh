package logger

import (
	"context"
	"os"

	"go.opentelemetry.io/otel/trace"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Logger 全局日志实例
var Logger *zap.Logger

// atomicLevel 原子日志级别（支持动态修改）
var atomicLevel zap.AtomicLevel

// Init 初始化日志
func Init(level string, format string) error {
	l, err := zapcore.ParseLevel(level)
	if err != nil {
		l = zapcore.InfoLevel
	}
	atomicLevel = zap.NewAtomicLevelAt(l)

	encoderConfig := zapcore.EncoderConfig{
		TimeKey:        "time",
		LevelKey:       "level",
		NameKey:        "logger",
		CallerKey:      "caller",
		FunctionKey:    zapcore.OmitKey,
		MessageKey:     "msg",
		StacktraceKey:  "stacktrace",
		LineEnding:     zapcore.DefaultLineEnding,
		EncodeLevel:    zapcore.LowercaseLevelEncoder,
		EncodeTime:     zapcore.ISO8601TimeEncoder,
		EncodeDuration: zapcore.SecondsDurationEncoder,
		EncodeCaller:   zapcore.ShortCallerEncoder,
	}

	var encoder zapcore.Encoder
	if format == "console" {
		encoder = zapcore.NewConsoleEncoder(encoderConfig)
	} else {
		encoder = zapcore.NewJSONEncoder(encoderConfig)
	}

	core := zapcore.NewCore(
		encoder,
		zapcore.AddSync(os.Stdout),
		atomicLevel, // 使用原子级别，支持动态修改
	)

	Logger = zap.New(core, zap.AddCaller(), zap.AddStacktrace(zapcore.ErrorLevel))
	return nil
}

// SetLevel 动态设置日志级别（用于热更新）
func SetLevel(level string) error {
	l, err := zapcore.ParseLevel(level)
	if err != nil {
		return err
	}
	atomicLevel.SetLevel(l)
	return nil
}

// GetLevel 获取当前日志级别
func GetLevel() string {
	return atomicLevel.Level().String()
}

// Debug 输出 Debug 级别日志
func Debug(msg string, fields ...zap.Field) {
	Logger.Debug(msg, fields...)
}

// Info 输出 Info 级别日志
func Info(msg string, fields ...zap.Field) {
	Logger.Info(msg, fields...)
}

// Warn 输出 Warn 级别日志
func Warn(msg string, fields ...zap.Field) {
	Logger.Warn(msg, fields...)
}

// Error 输出 Error 级别日志
func Error(msg string, fields ...zap.Field) {
	Logger.Error(msg, fields...)
}

// Fatal 输出 Fatal 级别日志并退出
func Fatal(msg string, fields ...zap.Field) {
	Logger.Fatal(msg, fields...)
}

// Sync 刷新日志缓冲区
func Sync() error {
	return Logger.Sync()
}

// Ctx 返回一个自动注入 trace_id 和 span_id 的 logger
// 使用方式: logger.Ctx(ctx).Info("message", zap.String("key", "value"))
// 输出示例: {"msg":"message","key":"value","trace_id":"abc123","span_id":"def456"}
func Ctx(ctx context.Context) *zap.Logger {
	if ctx == nil {
		return Logger
	}
	
	spanCtx := trace.SpanContextFromContext(ctx)
	if !spanCtx.IsValid() {
		return Logger
	}
	
	fields := make([]zap.Field, 0, 2)
	if spanCtx.HasTraceID() {
		fields = append(fields, zap.String("trace_id", spanCtx.TraceID().String()))
	}
	if spanCtx.HasSpanID() {
		fields = append(fields, zap.String("span_id", spanCtx.SpanID().String()))
	}
	
	if len(fields) == 0 {
		return Logger
	}
	
	return Logger.With(fields...)
}
