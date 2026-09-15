package claude

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
)

// setupRequestForForm is the SharedCell Setup the matrix uses, parameterized by
// form, so a presenting run resolves against EXACTLY the state a delivering run
// resolved against. That sameness is the premise of the whole form: the member
// can only name the session's files if it computes the same paths from the same
// inputs, and a test that varied the inputs would prove nothing. home is the
// session's relocated engine home, shared by every run within the session.
func setupRequestForForm(workDir, home string, form agent.LaunchForm) *agent.SetupRequest {
	return &agent.SetupRequest{
		WorkDir:   workDir,
		Env:       sessionEnv("perky-same-chevy", home),
		Fragments: []*agent.Fragment{{Content: "project rules"}},
		CellKind:  agent.CellKindShared,
		Form:      form,
		Managed: &agent.ManagedConfig{
			ManageStatusline: true,
			Hooks: &wire.HooksConfig{Unified: wire.UnifiedHooks{
				SessionStart: []wire.Hook{{Command: "ctxloom hook session-bind", Type: "command"}},
			}},
			BundleMCP: map[string]wire.MCPServer{"demo": {Command: "demo-server"}},
		},
	}
}

// TestPresentForm_NamesTheSessionsSurfaces is the end-to-end shape of a shared
// fan-out member: a session sets up, a member presents, and the member's argv
// names the SAME files the session wrote. If the two computed different paths
// the member would be pointing claude at files that do not exist — which the
// refusal below turns into a failure rather than a silent context-free launch.
func TestPresentForm_NamesTheSessionsSurfaces(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workDir, home := t.TempDir(), t.TempDir()

	session := NewClaudeCode()
	require.NoError(t, session.Setup(context.Background(), setupRequestForForm(workDir, home, agent.LaunchFormDeliver)))
	require.NotEmpty(t, settingsPathOf(session), "the session must actually have delivered, or the member has nothing to present")

	member := NewClaudeCode()
	require.NoError(t, member.Setup(context.Background(), setupRequestForForm(workDir, home, agent.LaunchFormPresent)))

	assert.Equal(t, settingsPathOf(session), settingsPathOf(member),
		"the member names the session's settings file, not one of its own")
	assert.Equal(t, mcpPathOf(session), mcpPathOf(member))
	assert.Equal(t, contextPathOf(session), contextPathOf(member),
		"the framed context is named by its own bytes, so the same context is the same file")

	args := member.buildArgs(&agent.ExecuteRequest{Mode: agent.ModeOneshot})
	assert.True(t, argPair(args, flagSettings, settingsPathOf(session)))
	assert.True(t, argPair(args, flagMCPConfig, mcpPathOf(session)))
	assert.True(t, argPair(args, flagAppendSystemFile, contextPathOf(session)))
}

// TestPresentForm_RefusesWhenTheSessionNeverSetUp is the hazard, at the real
// surfaces. A fresh project — nothing ever delivered — must FAIL, naming what
// is missing. The two tempting alternatives are both the silent degrade this
// form exists to remove: write the file (clobbering the one copy every run in
// the session reads) or push the content down some other route.
func TestPresentForm_RefusesWhenTheSessionNeverSetUp(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	member := NewClaudeCode()
	err := member.Setup(context.Background(), setupRequestForForm(t.TempDir(), t.TempDir(), agent.LaunchFormPresent))

	require.Error(t, err, "presenting a surface nobody delivered must refuse, not degrade")
	assert.ErrorIs(t, err, agent.ErrAbsentSharedSurface)
}

// TestPresentForm_EmitsNoFlagForASurfaceItCannotName is the emission/writing
// drift guard. A flag and the file it names are two halves of ONE decision, and
// a flag naming a file that was never written is "succeeds without doing the
// thing" — claude would fail to open it, or worse, silently launch without it.
// An empty MCP set has nothing on disk to name, so it must contribute no flag
// AND must not turn the absence into a refusal.
func TestPresentForm_EmitsNoFlagForASurfaceItCannotName(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workDir, home := t.TempDir(), t.TempDir()

	noMCP := func(form agent.LaunchForm) *agent.SetupRequest {
		req := setupRequestForForm(workDir, home, form)
		req.Managed.BundleMCP = nil
		return req
	}

	session := NewClaudeCode()
	require.NoError(t, session.Setup(context.Background(), noMCP(agent.LaunchFormDeliver)))

	member := NewClaudeCode()
	require.NoError(t, member.Setup(context.Background(), noMCP(agent.LaunchFormPresent)),
		"a surface with no bytes is nothing to present, not something missing")

	assert.Empty(t, mcpPathOf(member), "no MCP file was written, so there is no path to name")
	assert.NotContains(t, member.buildArgs(&agent.ExecuteRequest{Mode: agent.ModeOneshot}), flagMCPConfig,
		"and therefore no --mcp-config flag: claude must never be handed a flag naming a file that is not there")
}

// TestPresentForm_WritesNothingIntoTheProjectCwd pins the rationale the old code
// gave for bypassing delivery altogether — "writing per-member config there
// would clobber the one shared surface" — as an actual property. The commands
// surface has no out-of-cwd form, so on a delivering run it falls back to the
// loud well-known write into .claude/commands/; a presenting member must not
// make that write.
func TestPresentForm_WritesNothingIntoTheProjectCwd(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	workDir, home := t.TempDir(), t.TempDir()

	withCommands := func(form agent.LaunchForm) *agent.SetupRequest {
		req := setupRequestForForm(workDir, home, form)
		req.Managed.Commands = []agent.CommandExport{{Name: "demo", Content: "do the demo"}}
		return req
	}

	session := NewClaudeCode()
	require.NoError(t, session.Setup(context.Background(), withCommands(agent.LaunchFormDeliver)))

	before := treeOf(t, workDir)

	member := NewClaudeCode()
	require.NoError(t, member.Setup(context.Background(), withCommands(agent.LaunchFormPresent)))

	assert.Equal(t, before, treeOf(t, workDir),
		"a presenting member must leave the shared project cwd byte-for-byte as the session left it")
}

// treeOf snapshots every file under root with its contents, so a test can
// assert that a run changed NOTHING there. A dir listing alone would miss a
// rewrite of a file that already existed, which is exactly the clobber at issue.
func treeOf(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	require.NoError(t, filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		b, rerr := os.ReadFile(path)
		if rerr != nil {
			return rerr
		}
		rel, rerr := filepath.Rel(root, path)
		if rerr != nil {
			return rerr
		}
		out[rel] = string(b)
		return nil
	}))
	return out
}
