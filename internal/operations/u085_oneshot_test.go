package operations

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	pb "github.com/ctxloom/ctxloom/internal/lm/grpc"
	"github.com/ctxloom/ctxloom/internal/lm/isolation"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/signing"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// isolatedStubPolicy is stubPolicy's ISOLATED twin: its Name is not "none", so
// isolation.Isolated reports true and runResolvedAgent takes the per-member
// managed-config branch — the only branch that actually consults the executable
// trust gate. SpawnClient still mints a canned client, so nothing spawns.
type isolatedStubPolicy struct{ mk func() pb.Client }

func (isolatedStubPolicy) Name() string { return "worktree" }
func (p isolatedStubPolicy) PrepareWorkspace(_ context.Context, projectDir, _ string) (isolation.Workspace, error) {
	return stubWorkspace{dir: projectDir}, nil
}
func (p isolatedStubPolicy) SpawnClient(string, string, int, isolation.Workspace, map[string]string) (pb.Client, error) {
	return p.mk(), nil
}
func (isolatedStubPolicy) StartRunner(context.Context, string, string, int, isolation.Workspace, map[string]string) (*isolation.RunnerHandle, error) {
	return &isolation.RunnerHandle{Kill: func() {}, Wait: func() error { return nil }}, nil
}

// stubIsolatedPrepare swaps runResolvedAgent's isolation.Prepare seam for one
// that always yields an ISOLATED policy over the live project dir.
func stubIsolatedPrepare(t *testing.T, mk func() pb.Client) {
	t.Helper()
	prev := prepareIsolation
	prepareIsolation = func(_ context.Context, _ isolation.Axes, _ string, _ isolation.ImageConfig, projectDir, _ string, _ isolation.SessionState) (isolation.Policy, isolation.Workspace) {
		return isolatedStubPolicy{mk: mk}, stubWorkspace{dir: projectDir}
	}
	t.Cleanup(func() { prepareIsolation = prev })
}

// withheldOneshotProject seeds a loadable on-disk project whose `dev` profile
// pulls a local bundle carrying two MCP servers, one of which is REJECTED in the
// user's approval store. config.Load (which backends.AssembleManagedConfig calls
// on the isolated-member path) reads this tree, so the withhold happens for
// real inside the run rather than being simulated.
func withheldOneshotProject(t *testing.T) *config.Config {
	t.Helper()
	restore := config.SetLookPathForTesting(func(string) (string, error) { return "", exec.ErrNotFound })
	t.Cleanup(restore)
	projectDir := testsupport.ProjectDir(t)
	t.Setenv("SSH_AUTH_SOCK", "")

	appDir := filepath.Join(projectDir, ".ctxloom")
	bundlesDir := filepath.Join(appDir, "content", "bundles")
	profilesDir := filepath.Join(appDir, "profiles")
	require.NoError(t, os.MkdirAll(bundlesDir, 0o755))
	require.NoError(t, os.MkdirAll(profilesDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(bundlesDir, "mcp-bundle.yaml"), []byte(
		"name: mcp-bundle\nversion: \"1.0\"\n"+
			"fragments:\n  rules:\n    content: \"ONESHOT-RULE-BODY\"\n"+
			"mcp:\n"+
			"  quiet-server:\n    command: npx\n    args: [\"-y\", \"quiet\"]\n"+
			"  noisy-server:\n    command: npx\n    args: [\"-y\", \"noisy\"]\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "dev.yaml"), []byte(
		"name: dev\nbundles:\n  - mcp-bundle\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(appDir, "config.yaml"), []byte(
		"version: 5\nworkspace: worktree\n"), 0o644))

	installUnsignedRejection(t,
		trust.Ref{Bundle: "mcp-bundle", Kind: trust.KindMCP, Name: "noisy-server", IsLocal: true},
		signing.FormRaw, mcpPayloadOf(bundles.BundleMCP{Command: "npx", Args: []string{"-y", "noisy"}}))

	return config.NewFixture(config.Fixture{AppPaths: []string{appDir}, Workspace: "worktree"})
}

// TestRunOneshot_SurfacesWithheldExecutable pins U085-F16: RunOneshot builds an
// ExecutableTrustGate for an isolated oneshot and then discarded the object,
// keeping only its Gate() closure — so every executable the gate withheld from
// the member's per-member config was withheld with no diagnostic at all.
// docs/trust-model.md: a withhold must never be silent or reasonless.
func TestRunOneshot_SurfacesWithheldExecutable(t *testing.T) {
	resetStrictness(t)
	cfg := withheldOneshotProject(t)
	stubIsolatedPrepare(t, func() pb.Client { return &stubClient{out: "done"} })
	warnings := captureWarnings(t)

	res, err := RunOneshot(context.Background(), cfg, RunOneshotRequest{Profile: "dev", Task: "review"})
	require.NoError(t, err)
	require.NotNil(t, res)

	assert.Contains(t, warnings.String(), "noisy-server",
		"the withheld MCP executable must be named in an advisory, not dropped silently")
}

// TestRunResolvedAgent_RejectsUnknownPermissionPosture pins U085-F25: the ok
// bool from agent.ParsePermissionMode was discarded, so a MISSPELLED posture
// was indistinguishable from an unset one. Both parsed to PermissionDefault,
// which is not SafeHeadless, so both floored to PermissionBypass — a member
// whose author wrote "plna" for "plan" ran with the MOST permissive setting.
// LLMEntry.Permissions comes straight out of a hand-edited config.yaml and is
// validated nowhere on the way in, so the typo is reachable.
func TestRunResolvedAgent_RejectsUnknownPermissionPosture(t *testing.T) {
	run := func(t *testing.T, perms string) (*stubClient, *RunOneshotResult, error) {
		t.Helper()
		stub := &stubClient{out: "ran"}
		res, err := runResolvedAgent(context.Background(), resolvedRunRequest{
			Task:        "t",
			WorkDir:     t.TempDir(),
			Label:       "claude-fast",
			Backend:     "claude-code",
			Permissions: perms,
			Factory:     func(string, string, int) (pb.Client, error) { return stub, nil },
		})
		return stub, res, err
	}

	t.Run("a misspelled posture is refused, not floored to bypass", func(t *testing.T) {
		stub, res, err := run(t, "plna")
		require.Error(t, err, "an unrecognised posture must not silently become bypass")
		assert.Nil(t, res)
		assert.Contains(t, err.Error(), "plna", "the error must quote what was written")
		assert.Nil(t, stub.gotReq, "the engine must never have run")
	})

	t.Run("an unset posture still floors to bypass", func(t *testing.T) {
		stub, _, err := run(t, "")
		require.NoError(t, err, "unset is a legitimate input and keeps the headless floor")
		require.NotNil(t, stub.gotReq)
		assert.Equal(t, agent.PermissionBypass.String(), stub.gotReq.Options.PermissionMode)
	})

	t.Run("a recognised headless-safe posture is honoured", func(t *testing.T) {
		stub, _, err := run(t, "bypass")
		require.NoError(t, err)
		require.NotNil(t, stub.gotReq)
		assert.Equal(t, agent.PermissionBypass.String(), stub.gotReq.Options.PermissionMode)
	})
}

// TestRunResolvedAgent_ScalesVerbosityToWireLevel pins U085-F24 at the PUBLIC
// SEAM above the duplication. The row is a verbatim-identical duplicate — the
// literal 16 appeared as `uint32(v * 16)` in both `ctxloom run` and this
// oneshot/fan tail — so no parity test between the two could ever be red, and a
// test against the new shared constant could only fail to compile. What is
// pinnable is the seam the collapse must not move: the wire verbosity level a
// -v count actually reaches the engine as (llm.proto: 0=silent, 16=commands,
// 32=args, 48+=debug). Green before and after the collapse by construction.
func TestRunResolvedAgent_ScalesVerbosityToWireLevel(t *testing.T) {
	for _, tc := range []struct {
		vCount int
		want   uint32
	}{{0, 0}, {1, 16}, {2, 32}, {3, 48}} {
		stub := &stubClient{out: "ran"}
		_, err := runResolvedAgent(context.Background(), resolvedRunRequest{
			Task:      "t",
			WorkDir:   t.TempDir(),
			Label:     "claude-fast",
			Backend:   "claude-code",
			Verbosity: tc.vCount,
			Factory:   func(string, string, int) (pb.Client, error) { return stub, nil },
		})
		require.NoError(t, err)
		require.NotNil(t, stub.gotReq)
		assert.Equal(t, tc.want, stub.gotReq.Options.Verbosity,
			"-v x%d must reach the engine as wire verbosity %d", tc.vCount, tc.want)
	}
}
