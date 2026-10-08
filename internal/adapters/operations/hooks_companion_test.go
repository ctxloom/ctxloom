package operations

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/companions"
	"github.com/ctxloom/ctxloom/internal/adapters/selfexec"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/pkg/clifmt/clidiag"
)

func probeWithCandidates(cands ...bundles.CompanionCandidate) bundles.CompanionProber {
	return func(context.Context) (bundles.CompanionProbe, error) {
		return bundles.CompanionProbe{Candidates: cands}, nil
	}
}

// TestApplyHooks_AbsentOrProbeFailedCompanion_StillApplies: a registered
// companion that is not installed, or that answered it has no loadout,
// contributes nothing by fact, and one whose probe failed with nothing on
// record to carry has already been warned about by its probe. None of them
// refuses the apply.
func TestApplyHooks_AbsentOrProbeFailedCompanion_StillApplies(t *testing.T) {
	root, cfg := setupProject(t, "claude-code")
	t.Cleanup(selfexec.SetPathForTesting("ctxloom"))
	cfg = withCompanionProbe(t, cfg, probeWithCandidates(
		bundles.CompanionCandidate{Bin: "reprise", Reason: bundles.CandidateAbsent},
		bundles.CompanionCandidate{Bin: "wedged", Path: "/opt/bin/wedged", Reason: bundles.CandidateProbeFailed},
		bundles.CompanionCandidate{Bin: "plain", Path: "/opt/bin/plain", Reason: bundles.CandidateNoLoadout},
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

// ltkGuardEnvelope is an ltk loadout contributing one MCP server, as its
// probe prints it.
func ltkGuardEnvelope(t *testing.T) []byte {
	t.Helper()
	envelope := testsupport.RunLoadout("mcp:\n  ltk-guard:\n    command: ltk\n    args: [mcp]\n")
	return envelope
}

// applyWithLtkAnswering applies hooks through the REAL companion prober, with
// ltk registered and resolving at /opt/bin/ltk and its loadout probe answering out/err,
// returning the apply's outcome and every warning printed.
func applyWithLtkAnswering(t *testing.T, base *config.Config, root string, out []byte, perr error) (*ApplyHooksResult, string, error) {
	t.Helper()
	t.Cleanup(selfexec.SetPathForTesting("ctxloom"))
	t.Cleanup(companions.SetLookPathForTesting(func(bin string) (string, error) {
		if bin == "ltk" {
			return "/opt/bin/ltk", nil
		}
		return "", exec.ErrNotFound
	}))
	restoreProbe := companions.SetCompanionLoadoutOutputForTesting(func(string) ([]byte, error) { return out, perr })
	defer restoreProbe()
	var warnings bytes.Buffer
	restoreSink := clidiag.SetSink(&warnings)
	defer restoreSink()
	cfg := withCompanionProbe(t, base, func(ctx context.Context) (bundles.CompanionProbe, error) {
		return companions.Prober{}.ProbeCompanionLoadouts(ctx, []string{"ltk"})
	})
	result, err := ApplyHooks(context.Background(), engines.Registry(), ApplyHooksRequest{
		Cfg: cfg, Backend: "claude-code", WorkDir: root, RegenerateContext: true,
	})
	return result, warnings.String(), err
}

// TestApplyHooks_VerifiedCompanionProbeFails_WarnsAndApplies is
// unread-spectrum's ruled follow-on: a VERIFIED companion whose loadout probe
// errors or times out contributes something UNKNOWN. The apply does not block
// on it, and it says which companion and what must answer. (Carrying that
// companion's existing entries forward is deferred to the safefs writer's
// per-path ownership record.)
func TestApplyHooks_VerifiedCompanionProbeFails_WarnsAndApplies(t *testing.T) {
	root, base := setupProject(t, "claude-code")

	result, warned, err := applyWithLtkAnswering(t, base, root, nil, context.DeadlineExceeded)

	require.NoError(t, err, "an unknown contribution is warned about, not a refusal")
	assert.Equal(t, "applied", result.Status)
	assert.Contains(t, warned, `companion "ltk"`)
	assert.Contains(t, warned, "/opt/bin/ltk loadout --format yaml", "the warning names the remedy")
}

// TestApplyHooks_VerifiedCompanionAnswersNoLoadout_ContributesNothing is the
// other half of the split: a clean "no loadout support" answer is a fact, so
// the apply proceeds without that companion and its old entries go.
func TestApplyHooks_VerifiedCompanionAnswersNoLoadout_ContributesNothing(t *testing.T) {
	root, base := setupProject(t, "claude-code")
	_, _, err := applyWithLtkAnswering(t, base, root, ltkGuardEnvelope(t), nil)
	require.NoError(t, err)
	require.Contains(t, readFileString(t, filepath.Join(root, ".mcp.json")), "ltk-guard")

	answered := exec.Command("sh", "-c", "exit 2").Run()
	result, warned, err := applyWithLtkAnswering(t, base, root, nil, answered)

	require.NoError(t, err)
	assert.Equal(t, "applied", result.Status)
	assert.NotContains(t, readFileString(t, filepath.Join(root, ".mcp.json")), "ltk-guard", "a companion with no loadout contributes nothing")
	assert.NotContains(t, warned, `companion "ltk"`, "an answer is not a warning")
}

func readFileString(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path) //nolint:gosec // a test fixture path
	if errors.Is(err, os.ErrNotExist) {
		return ""
	}
	require.NoError(t, err)
	return string(b)
}
