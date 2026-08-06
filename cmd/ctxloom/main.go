package main

import (
	"fmt"
	"io"
	"os"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"

	"github.com/ctxloom/ctxloom/internal/cli"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/shared/envswitch"
	"github.com/ctxloom/ctxloom/internal/shared/logsink"
	"github.com/ctxloom/ctxloom/internal/shared/procsec"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

func main() {
	// Deny same-uid inspection of THIS process's /proc entry, first and for
	// every ctxloom process without exception. The exec-time environment is
	// already snapshotted in /proc/<pid>/environ by the time main runs and
	// os.Unsetenv cannot scrub it, so the window in which a credential stamped
	// there by the spawning seam is readable by a same-uid peer lasts until
	// this call lands — hence before any other startup work, and hence no
	// per-command allowlist: any ctxloom process can be the one holding the
	// coordinator credential.
	//
	// Reports through clidiag (inside HardenAtStartup) rather than zap because
	// this runs BEFORE zap.ReplaceGlobals below; a warning handed to the
	// not-yet-installed global logger would be dropped, and a bypass nobody
	// hears is indistinguishable from hardening that silently failed.
	procsec.HardenAtStartup("ctxloom")

	// Degraded mode from the environment, read BEFORE dispatch so the
	// pre-cobra window (config discovery, projectroot) already runs in the
	// right mode. CTXLOOM_DEGRADED=1 is the hook/generated-registration
	// mechanism (e.g. an MCP registration that must serve despite a broken
	// project); the persistent --degraded flag wins over it once parsed (see
	// cli root's PersistentPreRun). Deliberately NO config key: a broken
	// config cannot excuse itself.
	if envSwitchOn("CTXLOOM_DEGRADED", os.Stderr) {
		strictness.SetDegraded(true)
	}

	// Companion discovery off from the environment, read BEFORE dispatch for the
	// same reason: the pre-cobra window can already assemble context, and probing
	// EXECUTES the companion binaries on PATH. CTXLOOM_NO_COMPANIONS=1 is the
	// mechanism for a subprocess/CI that must not depend on what the host has
	// installed; the persistent --no-companions flag wins over it once parsed
	// (see cli root's PersistentPreRun).
	if envSwitchOn("CTXLOOM_NO_COMPANIONS", os.Stderr) {
		config.SetCompanionsDisabled(true)
	}

	// Initialize logging (verbose mode if CTXLOOM_VERBOSE=1), dispatch, flush,
	// exit — in that order, and with the exit as the LAST thing this process
	// does. See runCLI for why the flush cannot be a defer.
	os.Exit(runCLI(loggerConstructor(envSwitchOn("CTXLOOM_VERBOSE", os.Stderr)), cli.Run, os.Stderr))
}

// runCLI installs the process-wide logger, dispatches, then flushes the
// logger's sinks and returns the exit code.
//
// The flush is a plain statement on the return path, never a defer: main's
// last act is os.Exit, which runs no deferred functions, so a deferred flush
// is skipped on every non-zero exit — precisely the runs whose diagnostics
// matter. That is also why dispatch RETURNS a code instead of exiting: the
// exit and the teardown have to live in the same frame or the teardown is
// decorative.
func runCLI(construct func() (*zap.Logger, error), dispatch func() int, warn io.Writer) int {
	logger := buildLogger(construct, warn)
	zap.ReplaceGlobals(logger)
	code := dispatch()
	_ = logger.Sync()
	return code
}

// envSwitchOn reads one of the CTXLOOM_* boolean process switches and reports
// a value no boolean spelling covers, rather than treating it as off in
// silence. These switches are read before any flag is parsed, so this warning
// is the only feedback an operator who mistyped one will ever get: the mode
// simply would not engage, with nothing to distinguish that from the feature
// being broken.
func envSwitchOn(name string, warn io.Writer) bool {
	on, unrecognized := envswitch.On(name)
	if unrecognized != "" && warn != nil {
		fmt.Fprintf(warn, "ctxloom: warning: %s=%q is not an on/off value; treating it as off "+
			"(on: 1/true/yes/on, off: 0/false/no/off)\n", name, unrecognized)
	}
	return on
}

// loggerConstructor picks the process logger's build recipe: a development
// encoder at debug level when verbose, otherwise a production encoder at warn
// level. Either way the sink is ~/.ctxloom/logs/ctxloom.log and NEVER stderr —
// see paths.HomeLogFilePath for why stderr is not ours to write to. Verbose
// additionally tees to stderr: that switch is set by an operator who is asking
// for terminal output, which is a different thing from a hook emitting it
// unbidden.
func loggerConstructor(verbose bool) func() (*zap.Logger, error) {
	return func() (*zap.Logger, error) {
		sink, err := logSink()
		if err != nil {
			return nil, err
		}

		level, encoder := zapcore.WarnLevel, zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig())
		out := []zapcore.WriteSyncer{sink}
		if verbose {
			level, encoder = zapcore.DebugLevel, zapcore.NewConsoleEncoder(zap.NewDevelopmentEncoderConfig())
			out = append(out, zapcore.Lock(os.Stderr))
		}

		// ErrorOutput is zap's own failure channel (a sink that will not accept
		// a write). It defaults to stderr, so leaving it alone would reintroduce
		// exactly the corruption this sink exists to avoid, in the one case
		// where nobody is watching for it.
		return zap.New(
			zapcore.NewCore(encoder, zapcore.NewMultiWriteSyncer(out...), level),
			zap.ErrorOutput(sink),
			zap.AddCaller(),
			zap.AddStacktrace(zapcore.ErrorLevel),
		), nil
	}
}

// logSink opens the process log for append. The returned syncer is locked
// because one file backs every logger in the process.
func logSink() (zapcore.WriteSyncer, error) {
	f, err := logsink.Open()
	if err != nil {
		return nil, err
	}
	return zapcore.Lock(zapcore.AddSync(f)), nil
}

// buildLogger runs a zap constructor and NEVER returns nil. zap's
// constructors return (nil, err) on failure, and the result of this one is
// handed straight to zap.ReplaceGlobals: a nil there is not inert, it is a
// process-wide global whose first use — any of the zap.L()/zap.S() call sites,
// or main's own Sync — dereferences nil. A logger that could not be built is
// therefore replaced by a no-op logger, and the reason is reported on warn so
// the degradation is visible rather than silent. A nil warn stream discards
// the report without changing the fallback.
func buildLogger(construct func() (*zap.Logger, error), warn io.Writer) *zap.Logger {
	logger, err := construct()
	if logger == nil {
		if warn != nil {
			fmt.Fprintf(warn, "ctxloom: warning: could not initialize logging (%v); continuing without it\n", err)
		}
		return zap.NewNop()
	}
	return logger
}
