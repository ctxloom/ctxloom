// Package logging provides structured logging for SCM using zap.
package logging

import (
	"os"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// Log message constants for consistent structured logging.
const (
	// Fragment operations
	MsgFragmentLoaded     = "fragment_loaded"
	MsgFragmentNotFound   = "fragment_not_found"
	MsgFragmentParsed     = "fragment_parsed"
	MsgFragmentAssembled  = "fragment_assembled"

	// Variable operations
	MsgVariableSet        = "variable_set"
	MsgVariableRedefined  = "variable_redefined"
	MsgVariableUnexpanded = "variable_unexpanded"

	// Generator operations
	MsgGeneratorStarted   = "generator_started"
	MsgGeneratorCompleted = "generator_completed"
	MsgGeneratorFailed    = "generator_failed"

	// Persona operations
	MsgPersonaLoaded      = "persona_loaded"
	MsgPersonaNotFound    = "persona_not_found"

	// Plugin operations
	MsgPluginStarted      = "plugin_started"
	MsgPluginCompleted    = "plugin_completed"
	MsgPluginFailed       = "plugin_failed"

	// Config operations
	MsgConfigLoaded       = "config_loaded"
	MsgConfigSaved        = "config_saved"

	// Editor operations
	MsgEditorLaunched     = "editor_launched"
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

// WithComponent returns a logger with a component field.
func WithComponent(component string) *zap.Logger {
	return L().With(zap.String("component", component))
}

// Fields for common structured log fields.
func FragmentName(name string) zap.Field {
	return zap.String("fragment", name)
}

func GeneratorName(name string) zap.Field {
	return zap.String("generator", name)
}

func PersonaName(name string) zap.Field {
	return zap.String("persona", name)
}

func PluginName(name string) zap.Field {
	return zap.String("plugin", name)
}

func VariableName(name string) zap.Field {
	return zap.String("variable", name)
}

func VariableValue(value string) zap.Field {
	return zap.String("value", value)
}

func FilePath(path string) zap.Field {
	return zap.String("path", path)
}

func Command(cmd string) zap.Field {
	return zap.String("command", cmd)
}

func ErrorField(err error) zap.Field {
	return zap.Error(err)
}

func Duration(d int64) zap.Field {
	return zap.Int64("duration_ms", d)
}

func Count(n int) zap.Field {
	return zap.Int("count", n)
}

// IsVerbose returns true if verbose output should be shown.
func IsVerbose() bool {
	return os.Getenv("SCM_VERBOSE") == "1"
}
