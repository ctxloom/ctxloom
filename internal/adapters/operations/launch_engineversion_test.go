package operations

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/engineversion"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/launch/launchtest"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/sessionlock"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/pkg/clifmt/clidiag"
)

// topLevelRun starts a top-level run (depth 0, the originator's own) on the
// launchtest fixture's "pinned" label, minting into the REAL session index so
// what is asserted is what a later process — a turn-end hook — reads back.
func topLevelRun(t *testing.T, probe func(ctx context.Context, engine, binary string) (string, error)) (launch.Launch, *sessions.Entry) {
	t.Helper()
	testsupport.Isolate(t)
	env := launchtest.Deps(t)
	store, err := sessions.Open(nil)
	require.NoError(t, err)
	deps := env.Deps
	deps.Sessions = store
	deps.ProbeVersion = probe
	src := launch.Source{Profiles: []string{"base"}, Label: "pinned", Mode: engine.Interactive, WorkDir: env.Project}
	l, err := StartRun(context.Background(), deps, sessions.Seed{ProjectDir: env.Project}, src)
	require.NoError(t, err, "the run must start")
	t.Cleanup(func() {
		_ = launch.Discard(context.Background(), l)
		sessionlock.Release(l.Identity.Harp)
	})
	entry, err := GetSession(l.Identity.Harp)
	require.NoError(t, err)
	require.NotNil(t, entry)
	return l, entry
}

// A TOP-LEVEL session's record carries engine_version (task #27). Only
// delegated children used to record one, so every `ctxloom run` session read
// back with NoRecordedVersionError, and the turn-end hooks that read the
// ending turn's transcript (next-step, skill-mates) captured nothing on every
// top-level run. The version is the one of the binary the launch spawns —
// the label's binary_path here — not the engine's default install.
//
// MUTATION: drop the record from StartRun → the version assertion goes red.
// Probe the engine's default ("" binary) → the asked-binary assertion goes red.
func TestStartRun_TopLevelSessionRecordsTheLaunchedBinarysEngineVersion(t *testing.T) {
	var asked [][2]string
	l, entry := topLevelRun(t, func(_ context.Context, eng, binary string) (string, error) {
		asked = append(asked, [2]string{eng, binary})
		return "2.1.292", nil
	})
	assert.Equal(t, "2.1.292", entry.EngineVersion, "the INDEX must carry the version: a turn-end hook in another process reads it there")
	assert.Equal(t, [][2]string{{string(launchtest.EngineName), launchtest.PinnedBinary}}, asked,
		"probe exactly once, the engine the launch resolved, at the binary it spawns")
	assert.Equal(t, launchtest.EngineName, l.Engine)
}

// A failed probe does not fail the run: it warns, and records nothing — the
// read path's refusal is where an unknown version protects anything.
func TestStartRun_ProbeFailureWarnsAndStillRuns(t *testing.T) {
	var buf bytes.Buffer
	restore := clidiag.SetSink(&buf)
	defer restore()
	_, entry := topLevelRun(t, func(context.Context, string, string) (string, error) {
		return "", &engineversion.BinaryAbsentError{Engine: "fixture", Err: errors.New("not on PATH")}
	})
	assert.Empty(t, entry.EngineVersion, "a failed probe records NOTHING, never a guess")
	assert.Contains(t, buf.String(), "will not be readable back", "the user hears why this session's transcript will refuse")
}

// No probe composed (a preview) records nothing and execs nothing.
func TestStartRun_NoProbeRecordsNothing(t *testing.T) {
	_, entry := topLevelRun(t, nil)
	assert.Empty(t, entry.EngineVersion)
}
