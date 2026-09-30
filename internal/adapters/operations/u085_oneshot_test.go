package operations

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ctxloom/ctxloom/internal/core/agents"
	"github.com/ctxloom/ctxloom/internal/testsupport/bundletree"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/companions"
	"github.com/ctxloom/ctxloom/internal/adapters/signing"
	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/trust"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// withheldOneshotProject seeds a loadable on-disk project whose `dev` profile
// pulls a local bundle carrying two MCP servers, one of which is REJECTED in the
// user's approval store. the config read (which backends.AssembleManagedConfig calls
// on the isolated-member path) reads this tree, so the withhold happens for
// real inside the run rather than being simulated.
func withheldOneshotProject(t *testing.T) *config.Config {
	t.Helper()
	restore := companions.SetLookPathForTesting(func(string) (string, error) { return "", exec.ErrNotFound })
	t.Cleanup(restore)
	projectDir := testsupport.ProjectDir(t)
	t.Setenv("SSH_AUTH_SOCK", "")

	appDir := filepath.Join(projectDir, ".ctxloom")
	bundlesDir := authoredV1(appDir)
	profilesDir := filepath.Join(appDir, "profiles")
	require.NoError(t, os.MkdirAll(bundlesDir, 0o755))
	require.NoError(t, os.MkdirAll(profilesDir, 0o755))
	bundletree.WriteOS(t, bundlesDir, "mcp-bundle", "version: 1.0.0\n"+
		"fragments:\n  rules:\n    content: \"ONESHOT-RULE-BODY\"\n"+
		"mcp:\n"+
		"  quiet-server:\n    command: npx\n    args: [\"-y\", \"quiet\"]\n"+
		"  noisy-server:\n    command: npx\n    args: [\"-y\", \"noisy\"]\n")
	require.NoError(t, os.WriteFile(filepath.Join(profilesDir, "dev.yaml"), []byte(
		"name: dev\nbundles:\n  - mcp-bundle\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(appDir, "config.yaml"), []byte(
		"version: 5\nworkspace: worktree\n"), 0o644))

	installUnsignedRejection(t,
		trust.Ref{Bundle: "mcp-bundle", Kind: trust.KindMCP, Name: "noisy-server", IsLocal: true},
		signing.FormRaw, mcpPayloadOf(bundles.BundleMCP{Command: "npx", Args: []string{"-y", "noisy"}}))

	return realGated(gatedFixture(config.Fixture{
		AppPaths:  []string{appDir},
		Workspace: "worktree",
		// bypass: this test is about the withheld-executable warning, not
		// permission resolution.
		LM: config.LMConfig{
			Configs:  map[string]config.LLMConfig{"claude-code": {Type: "claude-code", Permissions: agents.LabelPermissions{Engine: map[string]any{"mode": "bypass"}}}},
			Defaults: config.RoleDefaults{Primary: "claude-code"},
		},
	}))
}

// TestOneShot_SurfacesWithheldExecutable: the managed surfaces a one-shot
// now receives are gated by the generation's executable trust gate, and a
// withheld executable is named in an advisory, never dropped silently.
func TestOneShot_SurfacesWithheldExecutable(t *testing.T) {
	resetStrictness(t)
	cfg := withheldOneshotProject(t)
	// A claude one-shot runs in its session home, which authenticates from
	// the setup-token in the env: give it one, or the launch is refused
	// before the surfaces are ever gated.
	t.Setenv("CLAUDE_CODE_OAUTH_TOKEN", tokenFixture)
	stub := &stubEngine{out: "done"}
	warnings := captureWarnings(t)

	o, err := testOneShot(t, cfg, nil, stub, launch.Source{Profiles: []string{"dev"}})
	require.NoError(t, err)
	out, err := o.Turn(context.Background(), "review")
	require.NoError(t, err)
	require.Equal(t, "done", out)

	assert.Contains(t, warnings.String(), "noisy-server",
		"the withheld MCP executable must be named in an advisory, not dropped silently")
}
