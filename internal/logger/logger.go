package logger

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// dailyRotatingFile is a zapcore.WriteSyncer that rolls to a new file at midnight
// (server local time). Filename pattern: quantix-YYYYMMDD.log inside logDir.
// Previously the filename was resolved once at startup and never changed,
// so all log output ended up in the file dated the day the process started.
type dailyRotatingFile struct {
	mu      sync.Mutex
	dir     string
	curDay  string
	curFile *os.File
}

func newDailyRotatingFile(dir string) (*dailyRotatingFile, error) {
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("create log dir %s: %w", dir, err)
	}
	r := &dailyRotatingFile{dir: dir}
	if err := r.rotateLocked(time.Now()); err != nil {
		return nil, err
	}
	return r, nil
}

// rotateLocked must be called with r.mu held.
func (r *dailyRotatingFile) rotateLocked(now time.Time) error {
	day := now.Format("20060102")
	if r.curFile != nil && r.curDay == day {
		return nil
	}
	if r.curFile != nil {
		_ = r.curFile.Close()
		r.curFile = nil
	}
	path := filepath.Join(r.dir, fmt.Sprintf("quantix-%s.log", day))
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("open log file %s: %w", path, err)
	}
	r.curFile = f
	r.curDay = day
	return nil
}

func (r *dailyRotatingFile) Write(p []byte) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if err := r.rotateLocked(time.Now()); err != nil {
		return 0, err
	}
	return r.curFile.Write(p)
}

func (r *dailyRotatingFile) Sync() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.curFile == nil {
		return nil
	}
	return r.curFile.Sync()
}

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
		if logDir == "" {
			return cfg.Build()
		}
		// Production with logDir: rotate file daily so cross-day runs split correctly.
		rot, err := newDailyRotatingFile(logDir)
		if err != nil {
			return nil, err
		}
		encoder := zapcore.NewJSONEncoder(cfg.EncoderConfig)
		fileCore := zapcore.NewCore(encoder, zapcore.AddSync(rot), lvl)
		stderrCore := zapcore.NewCore(encoder, zapcore.AddSync(os.Stderr), lvl)
		return zap.New(zapcore.NewTee(fileCore, stderrCore), zap.AddCaller(), zap.AddStacktrace(zap.ErrorLevel)), nil
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

	// File core: JSON-ish console format with daily rotation.
	rot, err := newDailyRotatingFile(logDir)
	if err != nil {
		return nil, err
	}

	fileEncoderCfg := zap.NewDevelopmentEncoderConfig()
	fileEncoderCfg.EncodeLevel = zapcore.CapitalLevelEncoder // no color for file
	fileEncoderCfg.EncodeTime = zapcore.ISO8601TimeEncoder

	fileCore := zapcore.NewCore(
		zapcore.NewConsoleEncoder(fileEncoderCfg),
		zapcore.AddSync(rot),
		lvl,
	)

	// When logDir is set, write ONLY to file (not stdout).
	// This prevents duplicate lines when nohup redirects stdout to the same file.
	return zap.New(fileCore, zap.AddCaller(), zap.AddStacktrace(zap.ErrorLevel)), nil
}

// NewFileLogger creates a logger that writes ONLY to the given file path.
// Used for isolated log streams (e.g., per-backtest logs) that should not
// mix with the main application log. Parent directory is created if missing.
func NewFileLogger(filePath, level string) (*zap.Logger, error) {
	lvl, err := zap.ParseAtomicLevel(level)
	if err != nil {
		lvl = zap.NewAtomicLevelAt(zap.InfoLevel)
	}

	if err := os.MkdirAll(filepath.Dir(filePath), 0755); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}
	f, err := os.OpenFile(filePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return nil, fmt.Errorf("open log file %s: %w", filePath, err)
	}

	encoderCfg := zap.NewDevelopmentEncoderConfig()
	encoderCfg.EncodeLevel = zapcore.CapitalLevelEncoder
	encoderCfg.EncodeTime = zapcore.ISO8601TimeEncoder

	core := zapcore.NewCore(
		zapcore.NewConsoleEncoder(encoderCfg),
		zapcore.AddSync(f),
		lvl,
	)
	return zap.New(core, zap.AddCaller(), zap.AddStacktrace(zap.ErrorLevel)), nil
}

// MustNew creates a logger or panics on failure.
func MustNew(env, level, logDir string) *zap.Logger {
	l, err := New(env, level, logDir)
	if err != nil {
		panic(err)
	}
	return l
}
