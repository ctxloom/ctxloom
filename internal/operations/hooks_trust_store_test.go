package operations

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/signing/countersign"
)

// corruptApprovalsRecord plants an unparseable .sig in dir, which is exactly
// what countersign.Store.Resolve calls StateUnreadable: a record that will
// not unarmor is indistinguishable from a SUPPRESSED REJECTION. It drives the
// unreadable state on the REAL filesystem with no chmod and no root-skip
// flakiness, which matters because ApplyHooks resolves its bundles, profiles
// and context file through os.Stat and cannot run over a MemMapFs.
func corruptApprovalsRecord(t *testing.T, dir string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "0badc0de.sig"), []byte("not an armored signature\n"), 0o644))
	// Guard the guard: if this ever stops being the unreadable state the
	// assertions below are vacuous, and would pass for the wrong reason.
	state, err := countersign.NewStore(dir, afero.NewOsFs()).Resolve()
	require.Equal(t, countersign.StateUnreadable, state)
	require.Error(t, err)
}

// TestApplyHooks_UnreadableApprovalsStore_IsNotReportedAsApplied is the
// fail-closed half of amused-fondue, asserted at the surface a user actually
// runs (`ctxloom manage hooks install`).
//
// An unreadable approvals store puts EffectiveTrust in its deny-all posture,
// so every fragment is withheld and regenerateContext produces zero of them.
// That was byte-identical to a legitimately empty profile set: ("", nil),
// which ApplyHooks reported as Status "applied", a nil error and an EMPTY
// ContextHash while every native-file backend's managed context was stripped
// to match. Exit 0, a success message, zero bytes -- the silent no-op.
//
// The distinguishing fact is the ClassTrust strictness finding EffectiveTrust
// records on the way past. Findings only become errors where
// FindingsError is called, which in production was `ctxloom run` and agent
// spawn and NOT the apply path, which is why the same fault aborted one
// loudly and the other silently.
func TestApplyHooks_UnreadableApprovalsStore_IsNotReportedAsApplied(t *testing.T) {
	resetStrictness(t)
	tmpDir := t.TempDir()
	writeBundleFixture(t, tmpDir)
	t.Cleanup(SetHomeApprovalsDirForTesting(t.TempDir()))
	corruptApprovalsRecord(t, filepath.Join(tmpDir, ".ctxloom", paths.ApprovalsDirName))

	cfg := fixtureConfig(tmpDir)
	result, err := ApplyHooks(context.Background(), ApplyHooksRequest{
		Backend:           "claude-code",
		RegenerateContext: true,
		ConfigLoader:      func() (*config.Config, error) { return cfg, nil },
		WorkDir:           tmpDir,
	})

	require.Error(t, err, "an approvals store this process cannot read must abort the apply, not be reported as applied")
	assert.Contains(t, err.Error(), "approvals store")
	if result != nil {
		assert.NotEqual(t, "applied", result.Status)
	}
}

// TestApplyHooks_ReadableApprovalsStore_StillApplies is the positive control
// for the gate above: the SAME fixture with an approvals store that reads
// fine must still apply and still write a non-empty context. Without it the
// test above is satisfiable by an ApplyHooks that fails on everything.
func TestApplyHooks_ReadableApprovalsStore_StillApplies(t *testing.T) {
	resetStrictness(t)
	tmpDir := t.TempDir()
	writeBundleFixture(t, tmpDir)
	t.Cleanup(SetHomeApprovalsDirForTesting(t.TempDir()))

	cfg := fixtureConfig(tmpDir)
	result, err := ApplyHooks(context.Background(), ApplyHooksRequest{
		Backend:           "claude-code",
		RegenerateContext: true,
		ConfigLoader:      func() (*config.Config, error) { return cfg, nil },
		WorkDir:           tmpDir,
	})
	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, "applied", result.Status)
	require.NotEmpty(t, result.ContextHash)

	contextBytes, rerr := os.ReadFile(filepath.Join(tmpDir, ".ctxloom", "cache", "context", result.ContextHash+".md"))
	require.NoError(t, rerr)
	assert.Contains(t, string(contextBytes), "ALPHA-ONE", "the control must actually deliver bytes, not merely exit 0")
}
