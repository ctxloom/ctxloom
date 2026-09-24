// Package logboot installs the process-wide zap logger every ctxloom-family
// binary runs under. Each binary's main calls Install once, before anything
// that might log. Without it, zap.L() is zap's no-op global, and every
// structured record the shared packages emit (a stalled lock wait's, among
// them) is dropped with nothing on disk.
package logboot

import (
	"os"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/ctxloom/ctxloom/internal/shared/logsink"
)

// Install builds prog's logger, makes it the process global, and returns the
// flush the caller runs on its way out.
//
// The sink is ~/.ctxloom/logs/<prog>.log, opened lazily (logsink.Lazy), and
// NEVER stderr: a ctxloom-family process is very often a hook or a statusline
// command whose stderr belongs to the calling engine (see
// paths.HomeLogFilePath). Non-verbose records warn and above as JSON. Verbose
// records debug and above through the console encoder and additionally tees to
// stderr: that switch is set by an operator who is asking for terminal output,
// which is a different thing from a hook emitting it unbidden.
//
// The logger cannot fail to build — the sink defers every filesystem step to
// the first write, and reports its own failure there — so there is no nil
// logger to guard against and no construction error to return.
//
// Call the returned flush as a plain statement before os.Exit, never in a
// defer: os.Exit runs no deferred functions, so a deferred flush is skipped on
// every non-zero exit — precisely the runs whose diagnostics matter.
func Install(prog string, verbose bool) (sync func()) {
	// Locked because one file backs every logger in the process.
	sink := zapcore.Lock(logsink.Lazy(prog))

	level, encoder := zapcore.WarnLevel, zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig())
	out := []zapcore.WriteSyncer{sink}
	if verbose {
		level, encoder = zapcore.DebugLevel, zapcore.NewConsoleEncoder(zap.NewDevelopmentEncoderConfig())
		out = append(out, zapcore.Lock(os.Stderr))
	}

	// ErrorOutput is zap's own failure channel (a sink that will not accept a
	// write). It defaults to stderr, so leaving it alone would reintroduce
	// exactly the corruption this sink exists to avoid, in the one case where
	// nobody is watching for it.
	logger := zap.New(
		zapcore.NewCore(encoder, zapcore.NewMultiWriteSyncer(out...), level),
		zap.ErrorOutput(sink),
		zap.AddCaller(),
		zap.AddStacktrace(zapcore.ErrorLevel),
	)
	zap.ReplaceGlobals(logger)
	return func() { _ = logger.Sync() }
}
