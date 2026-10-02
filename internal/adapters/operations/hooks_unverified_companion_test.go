package operations

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/selfexec"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/engines"
)

func probeWithCandidates(cands ...bundles.CompanionCandidate) bundles.CompanionProber {
	return func(context.Context) (bundles.CompanionProbe, error) {
		return bundles.CompanionProbe{Candidates: cands}, nil
	}
}

// TestApplyHooks_UnverifiableCompanion_LeavesEverySurfaceUnchanged is
// unread-spectrum: a companion on PATH that cannot be verified (unsigned,
// untrusted signer, tampered) never runs, so its hooks, MCP servers and
// context are UNKNOWN — not empty. Writing the surfaces anyway strips its
// contribution from them and reports success. Which surfaces it contributes to
// is unknowable without running it, so every one is left exactly as it was,
// and the apply says why, naming the companion and where to look.
func TestApplyHooks_UnverifiableCompanion_LeavesEverySurfaceUnchanged(t *testing.T) {
	root, base := setupProject(t, "claude-code")
	// A previous apply, made while ltk verified and ran: its contribution is
	// on disk. Guard the guard — without it there, "unchanged" proves nothing.
	verified := withCompanionProbe(t, base, func(context.Context) (bundles.CompanionProbe, error) {
		return bundles.CompanionProbe{Loadouts: []bundles.CompanionLoadout{{
			Bin: "ltk", Path: "/opt/bin/ltk",
			Document: []byte("run:\n  version: 1.0.0\n  mcp:\n    ltk-guard:\n      command: ltk\n      args: [mcp]\n"),
		}}}, nil
	})
	applyHooksHermetically(t, verified, root, "claude-code")
	mcp, err := os.ReadFile(filepath.Join(root, ".mcp.json"))
	require.NoError(t, err)
	require.Contains(t, string(mcp), "ltk-guard", "the seed apply must carry ltk's contribution")

	cfg := withCompanionProbe(t, base, probeWithCandidates(
		bundles.CompanionCandidate{Bin: "ltk", Path: "/opt/bin/ltk", Reason: bundles.CandidateUnconsented},
	))
	before := snapshotTree(t, afero.NewOsFs(), root)

	result, err := ApplyHooks(context.Background(), engines.Registry(), ApplyHooksRequest{
		Cfg: cfg, Backend: "claude-code", WorkDir: root, RegenerateContext: true,
	})

	assert.Equal(t, before, snapshotTree(t, afero.NewOsFs(), root), "no surface may be written while a companion's contribution is unknown")
	require.ErrorIs(t, err, ErrUnverifiedCompanion)
	assert.Contains(t, err.Error(), "ltk (/opt/bin/ltk)")
	assert.Contains(t, err.Error(), "ctxloom companion show")
	assert.Nil(t, result, "a refused apply reports no result a caller could mistake for an applied one")
}

// TestApplyHooks_AbsentOrProbeFailedCompanion_StillApplies is the control: a
// companion that is not installed contributes nothing by fact, and one that
// was verified and ran but produced no loadout is reported by its probe. Only
// an UNVERIFIABLE companion's contribution is unknown.
func TestApplyHooks_AbsentOrProbeFailedCompanion_StillApplies(t *testing.T) {
	root, cfg := setupProject(t, "claude-code")
	t.Cleanup(selfexec.SetPathForTesting("ctxloom"))
	cfg = withCompanionProbe(t, cfg, probeWithCandidates(
		bundles.CompanionCandidate{Bin: "reprise", Reason: bundles.CandidateAbsent},
		bundles.CompanionCandidate{Bin: "wedged", Path: "/opt/bin/wedged", Reason: bundles.CandidateProbeFailed},
	))

	result, err := ApplyHooks(context.Background(), engines.Registry(), ApplyHooksRequest{
		Cfg: cfg, Backend: "claude-code", WorkDir: root, RegenerateContext: true,
	})

	require.NoError(t, err)
	assert.Equal(t, "applied", result.Status)
}

// TestApplyHooks_SurfacesNeverNameTheRunningBinary is unread-spectrum's
// second half: a project-side apply run from a dev build must not repoint the
// project's editor sessions at that build. Every surface names the bare
// executable (agent.CtxloomCommand) and ctxloom's own MCP server is served by
// a session's endpoint, so at rest it renders nothing — neither may carry the
// path of the binary that happened to run the apply.
func TestApplyHooks_SurfacesNeverNameTheRunningBinary(t *testing.T) {
	root, cfg := setupProject(t, "claude-code")
	const devBuild = "/home/dev/workspace/ctxloom/bin/ctxloom"
	t.Cleanup(selfexec.SetPathForTesting(devBuild))
	running, err := os.Executable()
	require.NoError(t, err)
	cfg = withCompanionProbe(t, cfg, func(context.Context) (bundles.CompanionProbe, error) {
		return bundles.CompanionProbe{Loadouts: []bundles.CompanionLoadout{{
			Bin: "ctxloom", Path: devBuild, Self: true,
			Document: []byte("run:\n  version: 1.0.0\n  mcp:\n    ctxloom:\n      served_by: session-endpoint\n"),
		}}}, nil
	})

	_, err = ApplyHooks(context.Background(), engines.Registry(), ApplyHooksRequest{
		Cfg: cfg, Backend: "claude-code", WorkDir: root, RegenerateContext: true,
	})
	require.NoError(t, err)

	settings, err := os.ReadFile(filepath.Join(root, ".claude", "settings.json"))
	require.NoError(t, err)
	require.Contains(t, string(settings), `"ctxloom"`, "the apply must have written ctxloom's own hooks for this to prove anything")
	for path, body := range snapshotTree(t, afero.NewOsFs(), root) {
		assert.NotContains(t, body, devBuild, "%s names the binary that ran the apply", path)
		assert.NotContains(t, body, running, "%s names the binary that ran the apply", path)
	}
	if mcp, rerr := os.ReadFile(filepath.Join(root, ".mcp.json")); rerr == nil {
		assert.NotContains(t, string(mcp), `"ctxloom"`, "ctxloom's own server is served by a session's endpoint and renders nothing at rest")
	}
}
