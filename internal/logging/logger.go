// Package logging provides structured logging for SCM using zap.
package logging

import (
	"os"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Log message constants for consistent structured logging.
const (
	MsgVariableUnexpanded = "variable_unexpanded"
	MsgConfigLoaded       = "config_loaded"
)

var (
	// Global logger instance
	logger *zap.Logger
)

// Init initializes the global logger.
// Call this early in main().
func Init(verbose bool) error {
	var cfg zap.Config
	if verbose {
		cfg = zap.NewDevelopmentConfig()
		cfg.EncoderConfig.EncodeLevel = zapcore.CapitalColorLevelEncoder
	} else {
		cfg = zap.NewProductionConfig()
		cfg.Level = zap.NewAtomicLevelAt(zap.WarnLevel)
	}

	cfg.OutputPaths = []string{"stderr"}

	var err error
	logger, err = cfg.Build()
	if err != nil {
		return err
	}

	return nil
}

// InitNoop initializes a no-op logger for testing or quiet mode.
func InitNoop() {
	logger = zap.NewNop()
}

// L returns the global logger.
func L() *zap.Logger {
	if logger == nil {
		// Default to noop if not initialized
		logger = zap.NewNop()
	}
	return logger
}

// Sync flushes any buffered log entries.
func Sync() {
	if logger != nil {
		_ = logger.Sync()
	}
}

// Fields for common structured log fields.
func VariableName(name string) zap.Field {
	return zap.String("variable", name)
}

func FilePath(path string) zap.Field {
	return zap.String("path", path)
}

func ErrorField(err error) zap.Field {
	return zap.Error(err)
}

// IsVerbose returns true if verbose output should be shown.
func IsVerbose() bool {
	return os.Getenv("SCM_VERBOSE") == "1"
}
