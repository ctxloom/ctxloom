package claude

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/core/wire"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// dirPlace is a minimal placement writing into a fixed directory, used by
// the focused delivery tests to target an explicit dir without the agent
// package's unexported placement types.
type dirPlace struct{ dir string }

func (p dirPlace) Dir() string { return p.dir }

// sharedRoots is what setupClaudeInTempHome hands back: the two out-of-cwd
// roots a shared-cell Setup writes under, so a test can assert which one a
// surface landed beneath.
type sharedRoots struct {
	ephem string // the harp's ephemeral dir — the shared cell's Scratch (settings)
	home  string // the relocated engine home — the private root (context, mcp)
}

// setupClaudeInTempHome runs a claude Setup in a SharedCell (the default cell)
// for the given harp/work with the managed payload, keeping HarpEphemeralDir
// under a temp home so the out-of-cwd scratch never touches the real ~/.ctxloom,
// and advising a temp relocated engine home as the private root.
func setupClaudeInTempHome(t *testing.T, work, harp string, managed *agent.ManagedConfig) (*ClaudeCode, sharedRoots) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	home := t.TempDir()
	backend := NewClaudeCode()
	require.NoError(t, backend.Setup(context.Background(), &agent.SetupRequest{
		WorkDir:   work,
		Env:       sessionEnv(harp, home),
		Fragments: []*agent.Fragment{{Content: "project rules"}},
		Managed:   managed,
		CellKind:  agent.CellKindShared,
	}))
	ephem, err := paths.HarpEphemeralDir(harp)
	require.NoError(t, err)
	return backend, sharedRoots{ephem: ephem, home: home}
}

// setupClaudeIsolated runs a claude Setup in an isolated cell (worktree), where
// the project-file surfaces land as well-known files inside the private
// working dir and the private-root ones beneath the returned engine home.
func setupClaudeIsolated(t *testing.T, work string, managed *agent.ManagedConfig) (*ClaudeCode, string) {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	home := t.TempDir()
	backend := NewClaudeCode()
	require.NoError(t, backend.Setup(context.Background(), &agent.SetupRequest{
		WorkDir:   work,
		Env:       map[string]string{ConfigDirEnv: home},
		Fragments: []*agent.Fragment{{Content: "project rules"}},
		Managed:   managed,
		CellKind:  agent.CellKindDirectoryIsolated,
	}))
	return backend, home
}

// TestSetup_ContextUnderEngineHome_ProjectTreeClean pins the relocation: the
// framed context file lands under the run's PRIVATE engine home, NOT under the
// project tree's .ctxloom/cache/context and not under the harp's scratch, and
// no framed sysprompt file leaks into the working directory.
func TestSetup_ContextUnderEngineHome_ProjectTreeClean(t *testing.T) {
	work := t.TempDir()
	backend, roots := setupClaudeInTempHome(t, work, "perky-same-chevy", &agent.ManagedConfig{})

	framed := contextPathOf(backend)
	require.NotEmpty(t, framed, "Setup must materialize the framed context file")

	// Lands under the harp ephemeral dir.
	assert.True(t, strings.HasPrefix(framed, roots.home),
		"framed context must land under the engine home: got %q, want prefix %q", framed, roots.home)
	assertNoSyspromptUnder(t, roots.ephem)
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
	backend, roots := setupClaudeInTempHome(t, work, "perky-same-chevy", managed)

	// The live cwd stays clean: no .claude/settings.json written there.
	assert.NoFileExists(t, filepath.Join(work, ".claude", "settings.json"),
		"SharedCell must NOT write settings into the live cwd")

	// Settings land under the harp's private ephemeral dir and buildArgs points
	// --settings at them.
	settingsPath := settingsPathOf(backend)
	require.NotEmpty(t, settingsPath, "Setup must materialize the out-of-cwd settings file")
	assert.True(t, strings.HasPrefix(settingsPath, roots.ephem),
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
	backend, roots := setupClaudeInTempHome(t, work, "perky-same-chevy", managed)

	// The live cwd stays clean — same SharedCell invariant as the hooks case.
	assert.NoFileExists(t, filepath.Join(work, ".claude", "settings.json"),
		"SharedCell must NOT write settings into the live cwd")

	settingsPath := settingsPathOf(backend)
	require.NotEmpty(t, settingsPath, "Setup must materialize the out-of-cwd settings file")
	assert.True(t, strings.HasPrefix(settingsPath, roots.ephem),
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
		BundleMCP: map[string]wire.MCPServer{
			"srv": {Command: "run-srv"},
		},
	}
	backend, roots := setupClaudeInTempHome(t, work, "perky-same-chevy", managed)

	assert.NoFileExists(t, filepath.Join(work, ".mcp.json"),
		"SharedCell must NOT write .mcp.json into the live cwd")

	mcpPath := mcpPathOf(backend)
	require.NotEmpty(t, mcpPath, "Setup must materialize the out-of-cwd MCP file")
	assert.True(t, strings.HasPrefix(mcpPath, roots.home),
		"the private MCP file must live under the engine home: %q", mcpPath)
	assert.NoFileExists(t, filepath.Join(roots.ephem, ".mcp.json"),
		"the private root is the engine home, not the harp's scratch")
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

// TestSetup_IsolatedCell_WellKnownFilesAndOnlyTheMCPFlag proves the
// isolated-cell path: the project-file surfaces land as their engine
// well-known files IN the private working dir (.claude/settings.json,
// .claude/commands, CLAUDE.md), the default mcp form lands beneath the engine
// home and is the ONLY surface announced on argv.
func TestSetup_IsolatedCell_WellKnownFilesAndOnlyTheMCPFlag(t *testing.T) {
	work := t.TempDir()
	managed := &agent.ManagedConfig{
		ManageStatusline: true,
		Commands:         []agent.CommandExport{{Name: "demo", Content: "do a thing", Enabled: true}},
		Hooks: &wire.HooksConfig{Unified: wire.UnifiedHooks{
			SessionStart: []wire.Hook{{Command: "ctxloom hook session-bind", Type: "command"}},
		}},
		BundleMCP: map[string]wire.MCPServer{"srv": {Command: "run-srv"}},
	}
	backend, home := setupClaudeIsolated(t, work, managed)

	// Well-known files in the private cwd; the private MCP file beneath the
	// engine home and NOT in the checkout — for a container with workspace:
	// none the checkout is the live project mount.
	require.FileExists(t, filepath.Join(work, ".claude", "settings.json"))
	require.FileExists(t, filepath.Join(work, "CLAUDE.md"))
	require.FileExists(t, filepath.Join(home, ".mcp.json"))
	assert.NoFileExists(t, filepath.Join(work, ".mcp.json"),
		"the default mcp form must not land in an isolated cell's checkout")
	entries, err := os.ReadDir(filepath.Join(work, ".claude", "commands"))
	require.NoError(t, err)
	assert.NotEmpty(t, entries)

	// Context and settings keep their well-known form on an isolated cell, so
	// neither is announced. MCP is announced: its default form is the private
	// config file on EVERY launch.
	args := backend.buildArgs(&agent.ExecuteRequest{Mode: agent.ModeInteractive, CellKind: agent.CellKindDirectoryIsolated})
	assert.NotContains(t, args, "--append-system-prompt-file")
	assert.NotContains(t, args, "--settings")
	assert.True(t, argPair(args, "--mcp-config", filepath.Join(home, ".mcp.json")),
		"the default mcp form is announced on every cell: %v", args)

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
	backend, _ := setupClaudeIsolated(t, work, managed)

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
		BundleMCP: map[string]wire.MCPServer{"srv": {Command: "run-srv"}},
	}
	backend, _ := setupClaudeInTempHome(t, work, "perky-same-chevy", managed)

	framed := contextPathOf(backend)
	settingsPath := settingsPathOf(backend)
	mcpPath := mcpPathOf(backend)
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
		Env:       sessionEnv("witty-plain-crate", t.TempDir()),
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

// TestSetup_SharedCell_NoEngineHome_SelectsTheProjectFile is a DELIBERATE
// REVERSAL, recorded as one.
//
// This test was TestSetup_SharedCell_NoEngineHome_RefusesThePrivateDefault and
// required the opposite: a shared-cell Setup carrying no relocated engine home
// had to fail with ErrUnrootedEngineHome and write nothing, explicitly
// asserting "no fallback to the project file".
//
// That refusal shipped and immediately fired on runs that are specified to
// succeed — an ordinary `ctxloom run`, and an agent run declaring
// engine_home: host, which is an explicit OPT-OUT of a controlled home. The
// row carried a revisit clause for exactly that symptom ("if the refusal fires
// on ordinary runs with no isolation intent, the SCOPING CONDITION, not the
// root, is wrong"), and the human ruled on it: selection promotes a surface to
// a private-root approach ONLY when the run actually advised such a root.
//
// So this is no longer a fallback. Nothing is being degraded at delivery time:
// the run never selects the private approach in the first place, because it is
// not an approach this run can deliver. The project file is what the engine
// reads when there is no private home to read from, and it is what
// ErrUnrootedEngineHome's own message told a human to pick by hand.
//
// The harp-scratch assertion is KEPT and is the one that still guards
// something: rerouting to a THIRD location nobody named would be the silent
// substitution the no-degradation rule forbids.
func TestSetup_SharedCell_NoEngineHome_SelectsTheProjectFile(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	work := t.TempDir()
	const harp = "perky-same-chevy"
	backend := NewClaudeCode()
	require.NoError(t, backend.Setup(context.Background(), &agent.SetupRequest{
		WorkDir:   work,
		Env:       map[string]string{sessionHarpEnv: harp},
		Fragments: []*agent.Fragment{{Content: "project rules"}},
		Managed:   &agent.ManagedConfig{BundleMCP: map[string]wire.MCPServer{"srv": {Command: "run-srv"}}},
		CellKind:  agent.CellKindShared,
	}), "a run advising no engine home must still launch: it selects the approach it CAN deliver rather than refusing one it cannot")

	// It really delivered, and it delivered the project file.
	assert.FileExists(t, filepath.Join(work, ".mcp.json"),
		"with no private root advised, the MCP surface lands in the project file — the approach the engine reads when it has no relocated home")

	// And nowhere else. A third location nobody named is the silent
	// substitution this whole delivery rule exists to forbid.
	ephem, err := paths.HarpEphemeralDir(harp)
	require.NoError(t, err)
	assert.NoFileExists(t, filepath.Join(ephem, ".mcp.json"), "no reroute to the harp's scratch")
}

// TestSetup_NoFragmentsIsNotAnError keeps the guard above from becoming a new
// failure mode of its own: a project that legitimately configures NO context
// still sets up cleanly and simply emits no context flag.
func TestSetup_NoFragmentsIsNotAnError(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	backend := NewClaudeCode()
	require.NoError(t, backend.Setup(context.Background(), &agent.SetupRequest{
		WorkDir:   t.TempDir(),
		Env:       sessionEnv("witty-plain-crate", t.TempDir()),
		Fragments: nil,
		Managed:   &agent.ManagedConfig{},
		CellKind:  agent.CellKindShared,
	}))
	assert.Empty(t, contextPathOf(backend), "nothing was asked for, so nothing is delivered")

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

// setupClaudeIsolatedSelecting runs an isolated-cell (worktree) Setup with an
// EXPLICIT per-surface approach selection, which is the only way to reach the
// approach a shared launch would otherwise derive for itself.
func setupClaudeIsolatedSelecting(t *testing.T, work string, surfaces map[agent.SurfaceKind]string) *ClaudeCode {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	backend := NewClaudeCode()
	require.NoError(t, backend.Setup(context.Background(), &agent.SetupRequest{
		WorkDir:   work,
		Env:       map[string]string{ConfigDirEnv: t.TempDir()},
		Fragments: []*agent.Fragment{{Content: "project rules"}},
		Managed:   &agent.ManagedConfig{Surfaces: surfaces},
		CellKind:  agent.CellKindDirectoryIsolated,
	}))
	return backend
}

// TestSetup_IsolatedCell_SystemPromptIsHonouredNotConvertedToCLAUDEmd is
// feeble-sway's settle condition: an ISOLATED launch that selected
// context:system-prompt gets the framed sysprompt file and its launch flag —
// NOT a CLAUDE.md. Before the one-form change the isolated arm ran the
// approach's plain Deliver, which WAS the CLAUDE.md write, so a worktree or
// container launch silently received project memory instead of the system
// prompt it asked for.
func TestSetup_IsolatedCell_SystemPromptIsHonouredNotConvertedToCLAUDEmd(t *testing.T) {
	work := t.TempDir()
	backend := setupClaudeIsolatedSelecting(t, work, map[agent.SurfaceKind]string{
		agent.SurfaceContext: ApproachSystemPrompt,
	})

	framed := contextPathOf(backend)
	require.NotEmpty(t, framed, "selecting system-prompt must materialize the framed context file")
	assert.True(t, strings.HasSuffix(framed, agent.SCMFramedContextSuffix),
		"the delivered file must be the framed sysprompt, got %q", framed)
	data, err := os.ReadFile(framed)
	require.NoError(t, err)
	assert.Contains(t, string(data), "project rules")

	assert.NoFileExists(t, filepath.Join(work, ContextFileName),
		"selecting system-prompt must NOT be converted into a CLAUDE.md write")

	args := backend.buildArgs(&agent.ExecuteRequest{Mode: agent.ModeInteractive, CellKind: agent.CellKindDirectoryIsolated})
	assert.True(t, argPair(args, flagAppendSystemFile, framed),
		"the selected approach's file must be announced on argv, else the content never reaches the engine: %v", args)
}
