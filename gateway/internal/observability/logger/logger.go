// Package logger 提供一个面向 K8s 部署配置过的 zap logger：
// JSON 编码写到 stdout，带结构化字段。
package logger

import (
	"fmt"
	"os"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// New 创建一个生产风格的 zap logger。level 从字符串解析
// （debug/info/warn/error）。format 可选 "json"（默认）或 "console"。
//
// 每行日志都带 pod 字段，方便多副本日志按 pod 过滤。
func New(level, format, pod string) (*zap.Logger, error) {
	var lvl zapcore.Level
	if err := lvl.UnmarshalText([]byte(level)); err != nil {
		return nil, fmt.Errorf("logger: invalid level %q: %w", level, err)
	}

	encoderCfg := zap.NewProductionEncoderConfig()
	encoderCfg.TimeKey = "ts"
	encoderCfg.EncodeTime = zapcore.ISO8601TimeEncoder
	encoderCfg.EncodeDuration = zapcore.StringDurationEncoder

	var encoder zapcore.Encoder
	switch format {
	case "console":
		encoderCfg.EncodeLevel = zapcore.CapitalColorLevelEncoder
		encoder = zapcore.NewConsoleEncoder(encoderCfg)
	default:
		encoder = zapcore.NewJSONEncoder(encoderCfg)
	}

	core := zapcore.NewCore(encoder, zapcore.AddSync(os.Stdout), lvl)
	logger := zap.New(core, zap.AddCaller(), zap.AddStacktrace(zapcore.ErrorLevel))

	if pod != "" {
		logger = logger.With(zap.String("pod", pod))
	}

	return logger, nil
}
