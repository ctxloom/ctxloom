package operations

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	claudereader "github.com/ctxloom/ctxloom/internal/transcript/vendorreader/claude"
	codexreader "github.com/ctxloom/ctxloom/internal/transcript/vendorreader/codex"
	kiroreader "github.com/ctxloom/ctxloom/internal/transcript/vendorreader/kiro"
)

// mintTurnSession indexes a session for backend at the pinned engine version
// its reader is validated against, and returns the harp. The version is
// required rather than decorative: selection is (engine, RECORDED version) ->
// adapter, and an unrecorded version refuses outright.
func mintTurnSession(t *testing.T, backend, version string) string {
	t.Helper()
	mgr, err := sessions.Open("")
	require.NoError(t, err)
	entry, err := mgr.AssignHarp(t.TempDir(), backend)
	require.NoError(t, err)
	require.NoError(t, mgr.RecordEngineVersion(entry.HarpName, version))
	return entry.HarpName
}

// TestResolveTurnTranscript_SelectsTheReaderForTheSessionsOwnEngine is the
// whole point of this seam. The turn-boundary hooks are installed on EVERY
// hooking backend, so a reader chosen by assumption rather than by the
// session's recorded engine reads nothing at all on every engine but one —
// silently, since a hook that captures nothing still exits 0.
//
// The expected adapter is taken from each reader package's own
// VersionedAdapters rather than named here, so adding or re-versioning an
// engine's adapter cannot leave this test asserting a stale pairing.
//
// MUTATION — resolve reg/SelectAdapter from a fixed backend instead of
// entry.Backend — turns the two non-claude rows red.
func TestResolveTurnTranscript_SelectsTheReaderForTheSessionsOwnEngine(t *testing.T) {
	tests := []struct {
		backend string
		version string
		want    any
	}{
		{config.BackendClaudeCode, "2.1.214", claudereader.VersionedAdapters[0].Adapter},
		{"codex", "0.144.6", codexreader.VersionedAdapters[0].Adapter},
		{"kiro", "2.13.0", kiroreader.VersionedAdapters[0].Adapter},
	}
	for _, tc := range tests {
		t.Run(tc.backend, func(t *testing.T) {
			testsupport.Isolate(t)
			harp := mintTurnSession(t, tc.backend, tc.version)

			// An EXISTING path, so the resolution under test is the
			// adapter's and not the locator's fallback.
			src := filepath.Join(t.TempDir(), "vendor-transcript")
			require.NoError(t, os.WriteFile(src, []byte("{}\n"), 0o644))

			adapter, gotSrc, err := ResolveTurnTranscript(context.Background(), harp, src)
			require.NoError(t, err)
			assert.IsType(t, tc.want, adapter, "the reader must come from the session's own engine")
			assert.Equal(t, src, gotSrc)
		})
	}
}

// TestResolveTurnTranscript_RefusesAnUnrecordedEngineVersion keeps this seam
// on the same footing as every other vendor read: a session whose engine
// version is unknown is REFUSED rather than parsed by a guessed adapter. A
// guessed parser produces a transcript that looks fine, is not, and feeds a
// model — which is worse than the missed capture refusing costs.
//
// MUTATION — fall back to the newest adapter when SelectAdapter refuses —
// turns this red.
func TestResolveTurnTranscript_RefusesAnUnrecordedEngineVersion(t *testing.T) {
	testsupport.Isolate(t)
	mgr, err := sessions.Open("")
	require.NoError(t, err)
	entry, err := mgr.AssignHarp(t.TempDir(), config.BackendClaudeCode)
	require.NoError(t, err)

	src := filepath.Join(t.TempDir(), "vendor-transcript")
	require.NoError(t, os.WriteFile(src, []byte("{}\n"), 0o644))

	_, _, rerr := ResolveTurnTranscript(context.Background(), entry.HarpName, src)
	require.Error(t, rerr, "an unknown transcript format must refuse, never guess")
}

// TestResolveTurnTranscript_UnindexedHarpIsNamed pins that the failure states
// what was wrong. This runs inside a hook whose only user interface is the
// diagnostic line it leaves behind, so "captured nothing" without a reason is
// the silent no-op this project keeps paying for.
func TestResolveTurnTranscript_UnindexedHarpIsNamed(t *testing.T) {
	testsupport.Isolate(t)
	_, _, err := ResolveTurnTranscript(context.Background(), "no-such-harp", "")
	require.Error(t, err)
	assert.NotEmpty(t, err.Error())
}
