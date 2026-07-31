package main

import (
	"errors"
	"io"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
)

// recordingSyncer is a zapcore.WriteSyncer that records the ORDER of the
// events the flush contract depends on, so a test can tell "Sync ran" from
// "Sync ran after the command finished".
type recordingSyncer struct {
	events *[]string
}

func (r recordingSyncer) Write(p []byte) (int, error) { return len(p), nil }

func (r recordingSyncer) Sync() error {
	*r.events = append(*r.events, "sync")
	return nil
}

func loggerOver(syncer zapcore.WriteSyncer) *zap.Logger {
	return zap.New(zapcore.NewCore(
		zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), syncer, zapcore.DebugLevel))
}

// The logger's sinks must be flushed AFTER the command has run and BEFORE the
// process exits. main's last act is os.Exit, which runs no deferred functions,
// so a deferred flush is skipped on every failing run — the runs whose
// diagnostics matter most. Pinning the order (dispatch, then sync) is what
// stops the flush drifting back into a defer, or above the dispatch where it
// would flush nothing.
func TestRunCLI_FlushesAfterDispatchAndReturnsItsCode(t *testing.T) {
	defer zap.ReplaceGlobals(zap.NewNop())

	var events []string
	lg := loggerOver(recordingSyncer{events: &events})

	code := runCLI(
		func() (*zap.Logger, error) { return lg, nil },
		func() int { events = append(events, "dispatch"); return 7 },
		io.Discard,
	)

	if code != 7 {
		t.Errorf("runCLI returned %d, want the dispatcher's own code 7", code)
	}
	want := []string{"dispatch", "sync"}
	if len(events) != len(want) {
		t.Fatalf("event order = %v, want %v", events, want)
	}
	for i := range want {
		if events[i] != want[i] {
			t.Fatalf("event order = %v, want %v", events, want)
		}
	}
}

// The flush must survive the logger having failed to build: buildLogger's
// no-op fallback is still a *zap.Logger and Sync on it is still called, so a
// diagnostics failure cannot take the exit code with it.
func TestRunCLI_FailedLoggerStillDispatchesAndReturnsCode(t *testing.T) {
	defer zap.ReplaceGlobals(zap.NewNop())

	var warn = &strBuf{}
	code := runCLI(
		func() (*zap.Logger, error) { return nil, errors.New("no sink") },
		func() int { return 3 },
		warn,
	)
	if code != 3 {
		t.Errorf("runCLI returned %d, want 3", code)
	}
	if warn.s == "" {
		t.Error("a failed logger build was not reported")
	}
}

type strBuf struct{ s string }

func (b *strBuf) Write(p []byte) (int, error) { b.s += string(p); return len(p), nil }
