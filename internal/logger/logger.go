package logger

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// New creates a zap logger configured for the given environment.
// env should be "production" or anything else for development mode.
// If logDir is non-empty, logs are also written to a date-rotated file in that directory.
func New(env, level, logDir string) (*zap.Logger, error) {
	lvl, err := zap.ParseAtomicLevel(level)
	if err != nil {
		lvl = zap.NewAtomicLevelAt(zap.InfoLevel)
	}

	if env == "production" {
		cfg := zap.NewProductionConfig()
		cfg.Level = lvl
		if logDir != "" {
			fp, err := openLogFile(logDir)
			if err != nil {
				return nil, err
			}
			cfg.OutputPaths = append(cfg.OutputPaths, fp)
		}
		return cfg.Build()
	}

	// Development: human-readable console output
	encoderCfg := zap.NewDevelopmentEncoderConfig()
	encoderCfg.EncodeLevel = zapcore.CapitalColorLevelEncoder
	encoderCfg.EncodeTime = zapcore.ISO8601TimeEncoder

	consoleCore := zapcore.NewCore(
		zapcore.NewConsoleEncoder(encoderCfg),
		zapcore.AddSync(os.Stdout),
		lvl,
	)

	if logDir == "" {
		return zap.New(consoleCore, zap.AddCaller(), zap.AddStacktrace(zap.ErrorLevel)), nil
	}

	// File core: JSON format for easy parsing
	fp, err := openLogFile(logDir)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(fp, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}

	fileEncoderCfg := zap.NewDevelopmentEncoderConfig()
	fileEncoderCfg.EncodeLevel = zapcore.CapitalLevelEncoder // no color for file
	fileEncoderCfg.EncodeTime = zapcore.ISO8601TimeEncoder

	fileCore := zapcore.NewCore(
		zapcore.NewConsoleEncoder(fileEncoderCfg),
		zapcore.AddSync(f),
		lvl,
	)

	// Tee: write to both stdout and file
	tee := zapcore.NewTee(consoleCore, fileCore)
	return zap.New(tee, zap.AddCaller(), zap.AddStacktrace(zap.ErrorLevel)), nil
}

// openLogFile ensures the log directory exists and returns the log file path for today.
func openLogFile(logDir string) (string, error) {
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return "", fmt.Errorf("create log dir %s: %w", logDir, err)
	}
	filename := fmt.Sprintf("quantix-%s.log", time.Now().Format("20060102"))
	return filepath.Join(logDir, filename), nil
}

// MustNew creates a logger or panics on failure.
func MustNew(env, level, logDir string) *zap.Logger {
	l, err := New(env, level, logDir)
	if err != nil {
		panic(err)
	}
	return l
}
