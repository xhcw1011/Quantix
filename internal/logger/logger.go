package logger

import (
	"os"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// New creates a zap logger configured for the given environment.
// env should be "production" or anything else for development mode.
func New(env, level string) (*zap.Logger, error) {
	lvl, err := zap.ParseAtomicLevel(level)
	if err != nil {
		lvl = zap.NewAtomicLevelAt(zap.InfoLevel)
	}

	if env == "production" {
		cfg := zap.NewProductionConfig()
		cfg.Level = lvl
		return cfg.Build()
	}

	// Development: human-readable console output
	encoderCfg := zap.NewDevelopmentEncoderConfig()
	encoderCfg.EncodeLevel = zapcore.CapitalColorLevelEncoder
	encoderCfg.EncodeTime = zapcore.ISO8601TimeEncoder

	core := zapcore.NewCore(
		zapcore.NewConsoleEncoder(encoderCfg),
		zapcore.AddSync(os.Stdout),
		lvl,
	)
	return zap.New(core, zap.AddCaller(), zap.AddStacktrace(zap.ErrorLevel)), nil
}

// MustNew creates a logger or panics on failure.
func MustNew(env, level string) *zap.Logger {
	l, err := New(env, level)
	if err != nil {
		panic(err)
	}
	return l
}
