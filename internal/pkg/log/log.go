// Package log 提供 release-manager 系统的结构化日志。
//
// 基于 uber-go/zap 内核，通过 go-logr/zapr 适配器暴露 logr.Logger 接口。
// 下游消费者统一使用 logr.Logger，便于替换日志实现。
package log

import (
	"os"

	"github.com/go-logr/logr"
	"github.com/go-logr/zapr"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// New 根据配置创建 logr.Logger。
// level: debug, info, warn, error
// format: json, console
// output: stdout 或文件路径
func New(level, format, output string) (logr.Logger, error) {
	lvl, err := zapcore.ParseLevel(level)
	if err != nil {
		lvl = zapcore.InfoLevel
	}

	// 生产环境默认配置：ISO8601 时间戳 + 大写级别
	encoderCfg := zap.NewProductionEncoderConfig()
	encoderCfg.TimeKey = "timestamp"
	encoderCfg.EncodeTime = zapcore.ISO8601TimeEncoder
	encoderCfg.EncodeLevel = zapcore.CapitalLevelEncoder

	var encoder zapcore.Encoder
	switch format {
	case "console":
		encoder = zapcore.NewConsoleEncoder(encoderCfg)
	default:
		encoder = zapcore.NewJSONEncoder(encoderCfg)
	}

	// 输出到 stdout 或文件
	ws := zapcore.AddSync(os.Stdout)
	if output != "" && output != "stdout" {
		f, err := os.OpenFile(output, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			return logr.Logger{}, err
		}
		ws = zapcore.AddSync(f)
	}

	core := zapcore.NewCore(encoder, ws, lvl)
	zl := zap.New(core, zap.AddCaller())

	// 通过 zapr 适配器转换为 logr.Logger
	return zapr.NewLogger(zl), nil
}

// NewNop 返回一个丢弃所有日志的 logger（用于测试）。
func NewNop() logr.Logger {
	return logr.Discard()
}
