package logx

import (
	"strings"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Init returns a sugared zap logger configured by level string (debug/info/warn/error).
func Init(level string) (*zap.SugaredLogger, error) {
	lvl := zapcore.InfoLevel
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "debug":
		lvl = zapcore.DebugLevel
	case "warn":
		lvl = zapcore.WarnLevel
	case "error":
		lvl = zapcore.ErrorLevel
	case "info", "":
		lvl = zapcore.InfoLevel
	}
	cfg := zap.NewProductionConfig()
	cfg.Level = zap.NewAtomicLevelAt(lvl)
	lg, err := cfg.Build()
	if err != nil {
		return nil, err
	}
	return lg.Sugar(), nil
}
