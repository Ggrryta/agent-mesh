package logger

import (
	"context"
	"fmt"
	"io"

	"github.com/cloudwego/hertz/pkg/common/hlog"
)

// HertzLogger 适配 Hertz 的日志接口，桥接到 Zap
type HertzLogger struct{}

func (l *HertzLogger) SetLevel(lv hlog.Level)      {}
func (l *HertzLogger) SetOutput(writer io.Writer)  {}

func (l *HertzLogger) Trace(v ...interface{})                  {}
func (l *HertzLogger) Debug(v ...interface{})                  { Debug(fmt.Sprint(v...)) }
func (l *HertzLogger) Info(v ...interface{})                   { Info(fmt.Sprint(v...)) }
func (l *HertzLogger) Notice(v ...interface{})                 { Info(fmt.Sprint(v...)) }
func (l *HertzLogger) Warn(v ...interface{})                   { Warn(fmt.Sprint(v...)) }
func (l *HertzLogger) Error(v ...interface{})                  { Error(fmt.Sprint(v...)) }
func (l *HertzLogger) Fatal(v ...interface{})                  { Fatal(fmt.Sprint(v...)) }
func (l *HertzLogger) Tracef(format string, v ...interface{})  {}
func (l *HertzLogger) Debugf(format string, v ...interface{})  { Debug(fmt.Sprintf(format, v...)) }
func (l *HertzLogger) Infof(format string, v ...interface{})   { Info(fmt.Sprintf(format, v...)) }
func (l *HertzLogger) Noticef(format string, v ...interface{}) { Info(fmt.Sprintf(format, v...)) }
func (l *HertzLogger) Warnf(format string, v ...interface{})   { Warn(fmt.Sprintf(format, v...)) }
func (l *HertzLogger) Errorf(format string, v ...interface{})  { Error(fmt.Sprintf(format, v...)) }
func (l *HertzLogger) Fatalf(format string, v ...interface{})  { Fatal(fmt.Sprintf(format, v...)) }

func (l *HertzLogger) CtxTracef(ctx context.Context, format string, v ...interface{})  {}
func (l *HertzLogger) CtxDebugf(ctx context.Context, format string, v ...interface{})  { Debug(fmt.Sprintf(format, v...)) }
func (l *HertzLogger) CtxInfof(ctx context.Context, format string, v ...interface{})   { Info(fmt.Sprintf(format, v...)) }
func (l *HertzLogger) CtxNoticef(ctx context.Context, format string, v ...interface{}) { Info(fmt.Sprintf(format, v...)) }
func (l *HertzLogger) CtxWarnf(ctx context.Context, format string, v ...interface{})   { Warn(fmt.Sprintf(format, v...)) }
func (l *HertzLogger) CtxErrorf(ctx context.Context, format string, v ...interface{})  { Error(fmt.Sprintf(format, v...)) }
func (l *HertzLogger) CtxFatalf(ctx context.Context, format string, v ...interface{})  { Fatal(fmt.Sprintf(format, v...)) }
