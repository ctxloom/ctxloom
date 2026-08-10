package claude

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// dirPlace is a minimal agent.Placement writing into a fixed directory, used by
// the focused delivery tests to target an explicit dir without the agent
// package's unexported placement types.
type dirPlace struct{ dir string }

func (p dirPlace) Dir() string { return p.dir }

// setupClaudeInTempHome runs a claude Setup in a SharedCell (the default cell)
// for the given harp/work with the managed payload, keeping HarpEphemeralDir
// under a temp home so the out-of-cwd scratch never touches the real ~/.ctxloom.
// Returns the ephemeral dir.
func setupClaudeInTempHome(t *testing.T, work, harp string, managed *agent.ManagedConfig) (*ClaudeCode, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	backend := NewClaudeCode()
	require.NoError(t, backend.Setup(context.Background(), &agent.SetupRequest{
		WorkDir:   work,
		Env:       map[string]string{sessionHarpEnv: harp},
		Fragments: []*agent.Fragment{{Content: "project rules"}},
		Managed:   managed,
		CellKind:  agent.CellKindShared,
	}))
	ephem, err := paths.HarpEphemeralDir(harp)
	require.NoError(t, err)
	return backend, ephem
}

// setupClaudeIsolated runs a claude Setup in an isolated cell (worktree), where
// every surface lands as a well-known file inside the private working dir and no
// out-of-cwd launch flag is used.
func setupClaudeIsolated(t *testing.T, work string, managed *agent.ManagedConfig) *ClaudeCode {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	backend := NewClaudeCode()
	require.NoError(t, backend.Setup(context.Background(), &agent.SetupRequest{
		WorkDir:   work,
		Fragments: []*agent.Fragment{{Content: "project rules"}},
		Managed:   managed,
		CellKind:  agent.CellKindDirectoryIsolated,
	}))
	return backend
}

// TestSetup_ContextScratchUnderEphemeralDir_ProjectTreeClean pins the relocation:
// the framed context file lands under the harp's PRIVATE ephemeral dir, NOT under
// the project tree's .ctxloom/cache/context, and no framed sysprompt file leaks
// into the working directory.
func TestSetup_ContextScratchUnderEphemeralDir_ProjectTreeClean(t *testing.T) {
	work := t.TempDir()
	backend, ephem := setupClaudeInTempHome(t, work, "perky-same-chevy", &agent.ManagedConfig{})

	framed := backend.surfaces.Context.Path()
	require.NotEmpty(t, framed, "Setup must materialize the framed context file")

	// Lands under the harp ephemeral dir.
	assert.True(t, strings.HasPrefix(framed, ephem),
		"framed context must land under HarpEphemeralDir: got %q, want prefix %q", framed, ephem)
	data, err := os.ReadFile(framed)
	require.NoError(t, err)
	assert.Contains(t, string(data), "project rules")
	assert.Contains(t, string(data), agent.ProjectContextHeader, "the framed file carries the ctxloom framing")

	// NOT under the project tree's context cache, and no .sysprompt.md anywhere
	// beneath the working directory.
	cacheDir := filepath.Join(work, agent.SCMContextSubdir)
	assert.False(t, strings.HasPrefix(framed, cacheDir),
		"framed context must NOT land under the project-tree context cache")
	assertNoSyspromptUnder(t, work)
}

// assertNoSyspromptUnder walks dir and fails if any .sysprompt.md file exists —
// the project tree must stay free of the context scratch.
func assertNoSyspromptUnder(t *testing.T, dir string) {
	t.Helper()
	_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info == nil || info.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, agent.SCMFramedContextSuffix) {
			t.Errorf("context scratch leaked into the project tree: %s", path)
		}
		return nil
	})
}

// TestSetup_SharedCell_SettingsOutOfCwd proves the approved SharedCell change:
// claude's settings (hooks + statusline) are delivered to an OUT-OF-CWD scratch
// file (--settings), NOT .claude/settings.json in the live cwd, and the builtin
// session-bind hook survives while the context-injection hook stays absent
// (context rides --append-system-prompt-file, so MergeManaged is fed "").
func TestSetup_SharedCell_SettingsOutOfCwd(t *testing.T) {
	work := t.TempDir()
	managed := &agent.ManagedConfig{
		ManageStatusline: true,
		Hooks: &wire.HooksConfig{Unified: wire.UnifiedHooks{
			SessionStart: []wire.Hook{{Command: "ctxloom hook session-bind", Type: "command"}},
		}},
	}
	backend, ephem := setupClaudeInTempHome(t, work, "perky-same-chevy", managed)

	// The live cwd stays clean: no .claude/settings.json written there.
	assert.NoFileExists(t, filepath.Join(work, ".claude", "settings.json"),
		"SharedCell must NOT write settings into the live cwd")

	// Settings land under the harp's private ephemeral dir and buildArgs points
	// --settings at them.
	settingsPath := backend.surfaces.Settings.Path()
	require.NotEmpty(t, settingsPath, "Setup must materialize the out-of-cwd settings file")
	assert.True(t, strings.HasPrefix(settingsPath, ephem),
		"settings scratch must live under the harp ephemeral dir: %q", settingsPath)
	data, err := os.ReadFile(settingsPath)
	require.NoError(t, err)
	settings := string(data)
	assert.Contains(t, settings, "session-bind", "the builtin session-bind hook must survive")
	assert.NotContains(t, settings, "inject-context",
		"the context-injection hook must NOT be wired for claude")

	args := backend.buildArgs(&agent.ExecuteRequest{Mode: agent.ModeInteractive, CellKind: agent.CellKindShared})
	assert.True(t, argPair(args, "--settings", settingsPath),
		"a SharedCell run loads settings via --settings")
}

// TestSetup_SharedCell_DenyToolsInSettings is the deny-tools payload test for a
// SharedCell run: it proves ManagedConfig.DenyTools reaches the OUT-OF-CWD
// --settings file's permissions.deny — not just that Setup exits without
// error (ctxloom's characteristic silent-no-op failure mode is exit 0 /
// zero bytes delivered, so this asserts the actual JSON payload, not merely
// that the call succeeded).
func TestSetup_SharedCell_DenyToolsInSettings(t *testing.T) {
	work := t.TempDir()
	managed := &agent.ManagedConfig{DenyTools: []string{"Task"}}
	backend, ephem := setupClaudeInTempHome(t, work, "perky-same-chevy", managed)

	// The live cwd stays clean — same SharedCell invariant as the hooks case.
	assert.NoFileExists(t, filepath.Join(work, ".claude", "settings.json"),
		"SharedCell must NOT write settings into the live cwd")

	settingsPath := backend.surfaces.Settings.Path()
	require.NotEmpty(t, settingsPath, "Setup must materialize the out-of-cwd settings file")
	assert.True(t, strings.HasPrefix(settingsPath, ephem),
		"settings scratch must live under the harp ephemeral dir: %q", settingsPath)

	data, err := os.ReadFile(settingsPath)
	require.NoError(t, err)

	var parsed struct {
		Permissions struct {
			Deny []string `json:"deny"`
		} `json:"permissions"`
	}
	require.NoError(t, json.Unmarshal(data, &parsed))
	assert.Equal(t, []string{"Task"}, parsed.Permissions.Deny,
		"the resolved deny_tools must land verbatim in permissions.deny — payload, not just a successful write")

	args := backend.buildArgs(&agent.ExecuteRequest{Mode: agent.ModeInteractive, CellKind: agent.CellKindShared})
	assert.True(t, argPair(args, "--settings", settingsPath),
		"a SharedCell run loads the deny-carrying settings via --settings — the same flag delivers the deny")
}

// TestSetup_SharedCell_MCPOutOfCwd proves the approved SharedCell change for MCP:
// the managed .mcp.json rides an OUT-OF-CWD --mcp-config file, not the live cwd,
// and WITHOUT --strict-mcp-config so claude LAYERS ctxloom's servers on top of the
// user's project .mcp.json (merge, not replace). commands — which have no
// out-of-cwd flag — still land in the well-known .claude/commands via the loud
// Unsafe hatch.
func TestSetup_SharedCell_MCPOutOfCwd(t *testing.T) {
	work := t.TempDir()
	managed := &agent.ManagedConfig{
		Commands: []agent.CommandExport{{Name: "demo", Content: "do a thing", Enabled: true}},
		MCP: &wire.MCPConfig{Servers: map[string]wire.MCPServer{
			"srv": {Command: "run-srv"},
		}},
	}
	backend, ephem := setupClaudeInTempHome(t, work, "perky-same-chevy", managed)

	assert.NoFileExists(t, filepath.Join(work, ".mcp.json"),
		"SharedCell must NOT write .mcp.json into the live cwd")

	mcpPath := backend.surfaces.MCP.Path()
	require.NotEmpty(t, mcpPath, "Setup must materialize the out-of-cwd MCP file")
	assert.True(t, strings.HasPrefix(mcpPath, ephem),
		"MCP scratch must live under the harp ephemeral dir: %q", mcpPath)
	mcpData, err := os.ReadFile(mcpPath)
	require.NoError(t, err)
	assert.Contains(t, string(mcpData), "srv", "the managed MCP server must be written")

	// commands stay in-cwd (no out-of-cwd flag → warned Unsafe well-known write).
	entries, err := os.ReadDir(filepath.Join(work, ".claude", "commands"))
	require.NoError(t, err, "Setup must write command exports into .claude/commands")
	assert.NotEmpty(t, entries, "the demo command must be materialized")

	args := backend.buildArgs(&agent.ExecuteRequest{Mode: agent.ModeInteractive, CellKind: agent.CellKindShared})
	assert.True(t, argPair(args, "--mcp-config", mcpPath),
		"a SharedCell run loads MCP via --mcp-config")
	assert.NotContains(t, args, "--strict-mcp-config",
		"--mcp-config is NOT strict — ctxloom's servers merge with the user's project .mcp.json")
}

// TestSetup_IsolatedCell_WellKnownFilesNoFlags proves the isolated-cell path:
// every surface lands as its engine well-known file IN the private working dir
// (.claude/settings.json, .mcp.json, .claude/commands, CLAUDE.md) and buildArgs
// adds NONE of the out-of-cwd flags.
func TestSetup_IsolatedCell_WellKnownFilesNoFlags(t *testing.T) {
	work := t.TempDir()
	managed := &agent.ManagedConfig{
		ManageStatusline: true,
		Commands:         []agent.CommandExport{{Name: "demo", Content: "do a thing", Enabled: true}},
		Hooks: &wire.HooksConfig{Unified: wire.UnifiedHooks{
			SessionStart: []wire.Hook{{Command: "ctxloom hook session-bind", Type: "command"}},
		}},
		MCP: &wire.MCPConfig{Servers: map[string]wire.MCPServer{"srv": {Command: "run-srv"}}},
	}
	backend := setupClaudeIsolated(t, work, managed)

	// Well-known files in the private cwd.
	require.FileExists(t, filepath.Join(work, ".claude", "settings.json"))
	require.FileExists(t, filepath.Join(work, ".mcp.json"))
	require.FileExists(t, filepath.Join(work, "CLAUDE.md"))
	entries, err := os.ReadDir(filepath.Join(work, ".claude", "commands"))
	require.NoError(t, err)
	assert.NotEmpty(t, entries)

	// No out-of-cwd flags in an isolated cell.
	args := backend.buildArgs(&agent.ExecuteRequest{Mode: agent.ModeInteractive, CellKind: agent.CellKindDirectoryIsolated})
	assert.NotContains(t, args, "--append-system-prompt-file")
	assert.NotContains(t, args, "--mcp-config")
	assert.NotContains(t, args, "--settings")

	// No context scratch leaks into the tree (context is CLAUDE.md, not a sysprompt file).
	assertNoSyspromptUnder(t, work)
}

// TestSetup_IsolatedCell_DenyToolsInSettings is the isolated-cell twin of
// TestSetup_SharedCell_DenyToolsInSettings: the well-known
// .claude/settings.json written INTO the private working dir must carry
// permissions.deny — the deny-tools fix applies identically whether the
// cell is a SharedCell (out-of-cwd flag file) or an isolated cell
// (worktree/container well-known file).
func TestSetup_IsolatedCell_DenyToolsInSettings(t *testing.T) {
	work := t.TempDir()
	managed := &agent.ManagedConfig{DenyTools: []string{"Task"}}
	backend := setupClaudeIsolated(t, work, managed)

	settingsPath := filepath.Join(work, ".claude", "settings.json")
	require.FileExists(t, settingsPath)

	data, err := os.ReadFile(settingsPath)
	require.NoError(t, err)

	var parsed struct {
		Permissions struct {
			Deny []string `json:"deny"`
		} `json:"permissions"`
	}
	require.NoError(t, json.Unmarshal(data, &parsed))
	assert.Equal(t, []string{"Task"}, parsed.Permissions.Deny,
		"the resolved deny_tools must land verbatim in the well-known settings.json's permissions.deny")

	// No out-of-cwd flag in an isolated cell — same invariant as the plain case.
	args := backend.buildArgs(&agent.ExecuteRequest{Mode: agent.ModeInteractive, CellKind: agent.CellKindDirectoryIsolated})
	assert.NotContains(t, args, "--settings")
}

// TestCleanup_RemovesDeliveredSurfaces proves teardown reverses the delivered
// surfaces: the out-of-cwd context/settings/MCP scratch is removed on Cleanup.
func TestCleanup_RemovesDeliveredSurfaces(t *testing.T) {
	work := t.TempDir()
	managed := &agent.ManagedConfig{
		ManageStatusline: true,
		Hooks: &wire.HooksConfig{Unified: wire.UnifiedHooks{
			SessionStart: []wire.Hook{{Command: "ctxloom hook session-bind", Type: "command"}},
		}},
		MCP: &wire.MCPConfig{Servers: map[string]wire.MCPServer{"srv": {Command: "run-srv"}}},
	}
	backend, _ := setupClaudeInTempHome(t, work, "perky-same-chevy", managed)

	framed := backend.surfaces.Context.Path()
	settingsPath := backend.surfaces.Settings.Path()
	mcpPath := backend.surfaces.MCP.Path()
	require.FileExists(t, framed, "context scratch must exist after Setup")
	require.FileExists(t, settingsPath, "settings scratch must exist after Setup")
	require.FileExists(t, mcpPath, "MCP scratch must exist after Setup")

	require.NoError(t, backend.Cleanup(context.Background()))

	// The framed context scratch is a whole-file write, so cleanup removes it.
	assert.NoFileExists(t, framed, "Cleanup must remove the context scratch")

	// The settings/MCP surfaces revert the ctxloom-managed entries (a marker-merged
	// write), so the file may remain but must no longer carry ctxloom's content.
	if data, err := os.ReadFile(settingsPath); err == nil {
		assert.NotContains(t, string(data), "session-bind", "Cleanup must strip the ctxloom hooks")
	}
	if data, err := os.ReadFile(mcpPath); err == nil {
		assert.NotContains(t, string(data), "run-srv", "Cleanup must strip the ctxloom MCP server")
	}
}

// TestContextDelivery_DistinctHarpsDistinctScratch is the focused concurrency
// guard: two deliveries with DIFFERENT harps + DIFFERENT context strings write
// to DIFFERENT scratch paths under their own ephemeral roots — no collision, no
// shared-root write.
func TestContextDelivery_DistinctHarpsDistinctScratch(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	deliver := func(harp, content string) string {
		ephem, err := paths.HarpEphemeralDir(harp)
		require.NoError(t, err)
		strat := newAppendFlagDelivery(dirPlace{dir: ephem}, nil)
		_, err = strat.DeliverContext(content)
		require.NoError(t, err)
		p := strat.Path()
		require.NotEmpty(t, p)
		assert.True(t, strings.HasPrefix(p, ephem), "scratch must live under its own harp ephemeral dir")
		return p
	}

	a := deliver("harp-alpha", "context for alpha")
	b := deliver("harp-bravo", "context for bravo")

	assert.NotEqual(t, a, b, "distinct harps + content must write to distinct scratch paths")
	assert.NotEqual(t, filepath.Dir(a), filepath.Dir(b), "no shared scratch root across harps")
	require.FileExists(t, a)
	require.FileExists(t, b)
}

// TestSetup_FragmentsAssemblingToNothingIsLoud closes a real divergence: a
// backend whose context rides the RAW CACHE FILE (codex, kiro)
// already refuses this exact input: Provide → WriteContextFile
// returns agent.ErrNoContext, deliberately distinct from the no-fragments case,
// because "the user configured no context" and "every fragment the user
// configured resolved to nothing" are different facts. claude's context rides a
// surface instead, and that path assembled the same empty string, wrote no file,
// reported no flag, warned about nothing and returned nil — a session launched
// with zero bytes of the context the user asked for.
//
// The payload half is the companion below: nothing about this may make an
// ordinary delivery quieter.
func TestSetup_FragmentsAssemblingToNothingIsLoud(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := t.TempDir()
	backend := NewClaudeCode()
	err := backend.Setup(context.Background(), &agent.SetupRequest{
		WorkDir:   work,
		Env:       map[string]string{sessionHarpEnv: "witty-plain-crate"},
		Fragments: []*agent.Fragment{{Name: "rules", Content: ""}, {Name: "style", Content: "   \n\t "}},
		Managed:   &agent.ManagedConfig{},
		CellKind:  agent.CellKindShared,
	})
	require.Error(t, err, "two configured fragments delivering zero bytes must fail the launch, not launch context-less")
	assert.ErrorIs(t, err, agent.ErrNoContext,
		"the same fact must produce the same error as the raw-cache path, so callers can recognize it")
	assertNoSyspromptUnder(t, home)
	assertNoSyspromptUnder(t, work)
}

// TestSetup_NoFragmentsIsNotAnError keeps the guard above from becoming a new
// failure mode of its own: a project that legitimately configures NO context
// still sets up cleanly and simply emits no context flag.
func TestSetup_NoFragmentsIsNotAnError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	backend := NewClaudeCode()
	require.NoError(t, backend.Setup(context.Background(), &agent.SetupRequest{
		WorkDir:   t.TempDir(),
		Env:       map[string]string{sessionHarpEnv: "witty-plain-crate"},
		Fragments: nil,
		Managed:   &agent.ManagedConfig{},
		CellKind:  agent.CellKindShared,
	}))
	assert.Empty(t, backend.surfaces.Context.Path(), "nothing was asked for, so nothing is delivered")

	args := backend.buildArgs(&agent.ExecuteRequest{Mode: agent.ModeInteractive, CellKind: agent.CellKindShared})
	assert.NotContains(t, args, flagAppendSystemFile, "no context means no context flag")
}

// TestSetup_ContextPayloadStillReachesTheLaunchFlag is the payload assertion the
// guard must not weaken: a real fragment set is framed, written, and named on the
// argv that launches claude. Exit status alone would not notice its loss.
func TestSetup_ContextPayloadStillReachesTheLaunchFlag(t *testing.T) {
	backend, _ := setupClaudeInTempHome(t, t.TempDir(), "witty-plain-crate", &agent.ManagedConfig{})

	args := backend.buildArgs(&agent.ExecuteRequest{Mode: agent.ModeInteractive, CellKind: agent.CellKindShared})
	path := argValue(args, flagAppendSystemFile)
	require.NotEmpty(t, path, "the framed context file must be named on the argv")
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	assert.Contains(t, string(data), "project rules", "the fragment's own bytes must be in the delivered file")
}
