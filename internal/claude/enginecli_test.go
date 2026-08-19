package claude

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/wire"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// argvCase is one point in the buildArgs matrix, carrying a label the failure
// output can name.
type argvCase struct {
	label   string
	surface agent.CLISurface
	req     *agent.ExecuteRequest
}

// buildArgsMatrix enumerates EVERY combination buildArgs branches on —
// permission posture × mode × SkipSetup × CellKind — with a harp in the env
// (so the interactive --name arm fires) and a prompt (so the positional arm
// fires). That is the full space of argv shapes the driver can produce, modulo
// the opaque ClaudeConfig.Args passthrough, which is user-supplied and
// undeclarable by construction (left empty here).
func buildArgsMatrix() []argvCase {
	perms := []struct {
		name string
		p    agent.PermissionMode
	}{
		{"default", agent.PermissionDefault},
		{"bypass", agent.PermissionBypass},
		{"acceptEdits", agent.PermissionAcceptEdits},
		{"plan", agent.PermissionPlan},
	}
	modes := []struct {
		name    string
		m       agent.ExecutionMode
		surface agent.CLISurface
	}{
		{"oneshot", agent.ModeOneshot, agent.CLISurfaceOneshot},
		{"interactive", agent.ModeInteractive, agent.CLISurfaceInteractive},
	}
	cells := []struct {
		name string
		k    agent.CellKind
	}{
		{"shared", agent.CellKindShared},
		{"dir-isolated", agent.CellKindDirectoryIsolated},
		{"process-isolated", agent.CellKindProcessIsolated},
	}
	var out []argvCase
	for _, perm := range perms {
		for _, mode := range modes {
			for _, skip := range []bool{false, true} {
				for _, cell := range cells {
					out = append(out, argvCase{
						label:   fmt.Sprintf("%s/%s/skipSetup=%v/%s", perm.name, mode.name, skip, cell.name),
						surface: mode.surface,
						req: &agent.ExecuteRequest{
							Mode:        mode.m,
							Permissions: perm.p,
							SkipSetup:   skip,
							CellKind:    cell.k,
							Model:       "claude-opus-4-8",
							Env:         map[string]string{sessionHarpEnv: "perky-same-chevy"},
							Prompt:      &agent.Fragment{Content: "do the thing"},
						},
					})
				}
			}
		}
	}
	return out
}

// setupBackendForMatrix runs a SharedCell Setup with a non-empty context, hook
// set, and MCP config, so all three out-of-cwd launch-flag surfaces
// (--append-system-prompt-file, --mcp-config, --settings) have a real path and
// actually appear in the matrix's argv. Without this the shared-cell arm of
// buildArgs emits nothing and the test would silently cover less than it claims.
func setupBackendForMatrix(t *testing.T) *ClaudeCode {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	b := NewClaudeCode()
	require.NoError(t, b.Setup(context.Background(), &agent.SetupRequest{
		WorkDir:   t.TempDir(),
		Env:       map[string]string{sessionHarpEnv: "perky-same-chevy"},
		Fragments: []*agent.Fragment{{Content: "project rules"}},
		CellKind:  agent.CellKindShared,
		Managed: &agent.ManagedConfig{
			ManageStatusline: true,
			Hooks: &wire.HooksConfig{Unified: wire.UnifiedHooks{
				SessionStart: []wire.Hook{{Command: "ctxloom hook session-bind", Type: "command"}},
			}},
			BundleMCP: map[string]wire.MCPServer{
				"demo": {Command: "demo-server"},
			},
		},
	}))
	require.NotEmpty(t, b.surfaces.Context.Path(), "matrix needs a delivered context path")
	require.NotEmpty(t, b.surfaces.MCP.Path(), "matrix needs a delivered mcp path")
	require.NotEmpty(t, b.surfaces.Settings.Path(), "matrix needs a delivered settings path")
	return b
}

// TestEngineCLI_BuildArgsFlagsAreDeclared is the ANTI-DRIFT GATE. Every flag
// buildArgs can emit, across the full permission × mode × SkipSetup × CellKind
// matrix, must parse against the declared engine CLI grammar. A flag added to
// the driver without a declaration fails HERE, at the driver, instead of
// silently going missing from a stand-in binary that would keep reporting green.
//
// This is a real gate, not a coincidental pass: the assertion runs the driver's
// own output through agent.EngineCLI.ParseArgv, which errors on any token that
// starts with "-" and is not declared.
func TestEngineCLI_BuildArgsFlagsAreDeclared(t *testing.T) {
	b := setupBackendForMatrix(t)
	clis := b.EngineCLIs()

	for _, c := range buildArgsMatrix() {
		t.Run(c.label, func(t *testing.T) {
			cli, ok := agent.EngineCLIFor(clis, c.surface)
			require.True(t, ok, "no declaration for surface %s", c.surface)
			require.NoError(t, cli.Validate())

			args := b.buildArgs(c.req)
			_, err := cli.ParseArgv(args)
			require.NoError(t, err, "buildArgs emitted argv the contract cannot read: %v", args)
		})
	}
}

// TestEngineCLI_EveryDeclaredFlagIsEmitted closes the other direction: a
// declaration that outgrew the driver is dead weight that a fake would honour
// and nothing would ever produce. Every declared flag (bar those marked
// Ignored, which exist only for vendor-grammar fidelity) must appear somewhere
// in the matrix.
func TestEngineCLI_EveryDeclaredFlagIsEmitted(t *testing.T) {
	b := setupBackendForMatrix(t)
	clis := b.EngineCLIs()

	emitted := map[agent.CLISurface]map[string]bool{}
	for _, c := range buildArgsMatrix() {
		if emitted[c.surface] == nil {
			emitted[c.surface] = map[string]bool{}
		}
		for _, a := range b.buildArgs(c.req) {
			emitted[c.surface][a] = true
		}
	}

	for _, cli := range clis {
		for _, f := range cli.Flags {
			if f.Ignored {
				continue
			}
			assert.True(t, emitted[cli.Surface][f.Name],
				"%s declares %s but the driver never emits it on that surface", cli.Surface, f.Name)
		}
	}
}

// TestEngineCLI_SurfaceExclusiveFlags pins the two flags that must NOT cross
// surfaces: --print is oneshot-only and --name is interactive-only. The
// contract states it; ParseArgv enforces it, because feeding an interactive
// argv to the oneshot grammar (or vice versa) is exactly how a stand-in binary
// would be spawned.
func TestEngineCLI_SurfaceExclusiveFlags(t *testing.T) {
	b := NewClaudeCode()
	clis := b.EngineCLIs()
	oneshot, ok := agent.EngineCLIFor(clis, agent.CLISurfaceOneshot)
	require.True(t, ok)
	interactive, ok := agent.EngineCLIFor(clis, agent.CLISurfaceInteractive)
	require.True(t, ok)

	_, hasName := oneshot.LookupFlag(flagName)
	assert.False(t, hasName, "--name is interactive-only")
	_, hasPrint := interactive.LookupFlag(flagPrint)
	assert.False(t, hasPrint, "--print is oneshot-only")

	// The interactive line (with --name) must not parse as a oneshot line.
	iArgs := b.buildArgs(&agent.ExecuteRequest{
		Mode: agent.ModeInteractive,
		Env:  map[string]string{sessionHarpEnv: "perky-same-chevy"},
	})
	_, err := oneshot.ParseArgv(iArgs)
	var undeclared *agent.UndeclaredFlagError
	require.ErrorAs(t, err, &undeclared)
	assert.Equal(t, flagName, undeclared.Flag)
}

// TestEngineCLI_PromptDeliveryMatchesDriver pins the fact a naive fake gets
// wrong: the oneshot task travels on STDIN (argv delivery hit E2BIG on
// `ctxloom weave` synthesis), the interactive prompt on argv. The declaration
// and the driver are asserted against each other, so neither can move alone.
func TestEngineCLI_PromptDeliveryMatchesDriver(t *testing.T) {
	b := NewClaudeCode()
	clis := b.EngineCLIs()
	const task = "review this enormous diff"
	prompt := &agent.Fragment{Content: task}

	oneshot, _ := agent.EngineCLIFor(clis, agent.CLISurfaceOneshot)
	assert.Equal(t, agent.PromptStdin, oneshot.Prompt)
	oArgs := b.buildArgs(&agent.ExecuteRequest{Mode: agent.ModeOneshot, Prompt: prompt})
	oParsed, err := oneshot.ParseArgv(oArgs)
	require.NoError(t, err)
	assert.Empty(t, oParsed.Positionals, "oneshot declares PromptStdin, so argv carries no positional")
	assert.NotNil(t, promptStdin(&agent.ExecuteRequest{Prompt: prompt}), "and the driver does put it on stdin")

	interactive, _ := agent.EngineCLIFor(clis, agent.CLISurfaceInteractive)
	assert.Equal(t, agent.PromptPositional, interactive.Prompt)
	iArgs := b.buildArgs(&agent.ExecuteRequest{Mode: agent.ModeInteractive, Prompt: prompt})
	iParsed, err := interactive.ParseArgv(iArgs)
	require.NoError(t, err)
	assert.Equal(t, []string{task}, iParsed.Positionals)
}

// TestEngineCLI_SettingsValueShapeCoversBothForms pins the trap: --settings
// takes a FILE PATH on the normal delivery path and a LITERAL inline JSON
// object under SkipSetup. A grammar declaring "path" would be wrong half the
// time, so the declaration says path-or-json and both driver forms are proved.
func TestEngineCLI_SettingsValueShapeCoversBothForms(t *testing.T) {
	b := setupBackendForMatrix(t)
	clis := b.EngineCLIs()
	oneshot, _ := agent.EngineCLIFor(clis, agent.CLISurfaceOneshot)

	f, ok := oneshot.LookupFlag(flagSettings)
	require.True(t, ok)
	assert.Equal(t, agent.ValuePathOrJSON, f.Value)

	sharedArgs := b.buildArgs(&agent.ExecuteRequest{Mode: agent.ModeOneshot, CellKind: agent.CellKindShared})
	shared, err := oneshot.ParseArgv(sharedArgs)
	require.NoError(t, err)
	v, ok := shared.Value(flagSettings)
	require.True(t, ok)
	assert.True(t, filepath.IsAbs(v), "normal delivery passes a file PATH, got %q", v)

	minArgs := b.buildArgs(&agent.ExecuteRequest{Mode: agent.ModeOneshot, SkipSetup: true})
	minimal, err := oneshot.ParseArgv(minArgs)
	require.NoError(t, err)
	v, ok = minimal.Value(flagSettings)
	require.True(t, ok)
	assert.Equal(t, byte('{'), v[0], "SkipSetup passes an inline JSON OBJECT, got %q", v)
}

// TestEngineCLI_EmptyStringValuesStayTheirOwnToken pins claude's two empty-string
// flags: `--tools ""` and `--system-prompt ""` pass an EMPTY STRING as a
// SEPARATE argv token. A fake treating the empty token as absence would report
// tools enabled on a run that disabled them.
func TestEngineCLI_EmptyStringValuesStayTheirOwnToken(t *testing.T) {
	b := NewClaudeCode()
	clis := b.EngineCLIs()
	oneshot, _ := agent.EngineCLIFor(clis, agent.CLISurfaceOneshot)

	parsed, err := oneshot.ParseArgv(b.buildArgs(&agent.ExecuteRequest{Mode: agent.ModeOneshot, SkipSetup: true}))
	require.NoError(t, err)
	for _, name := range []string{flagTools, flagSystemPrompt} {
		v, ok := parsed.Value(name)
		assert.True(t, ok, "%s must be present", name)
		assert.Equal(t, "", v, "%s carries an empty string as its own token", name)
	}
}

// TestEngineCLI_ProbesMatchTheWriters pins the declared probe paths to the
// paths ctxloom's writers actually target, so the declaration cannot describe a
// file nothing writes.
func TestEngineCLI_ProbesMatchTheWriters(t *testing.T) {
	clis := ClaudeEngineCLIs()
	cli, ok := agent.EngineCLIFor(clis, agent.CLISurfaceInteractive)
	require.True(t, ok)
	require.NoError(t, cli.Validate())

	const dir = "/work"
	w := &ClaudeCodeHookWriter{}

	byKind := func(kind agent.ProbeKind) agent.CLIProbe {
		for _, p := range cli.ProbesFor(kind) {
			if p.Scope == agent.ScopeCwd {
				return p
			}
		}
		t.Fatalf("no cwd probe declared for %s", kind)
		return agent.CLIProbe{}
	}

	assert.Equal(t, w.SettingsPath(dir), filepath.Join(dir, byKind(agent.ProbeKindSettings).Rel))
	assert.Equal(t, w.MCPConfigPath(dir), filepath.Join(dir, byKind(agent.ProbeKindMCP).Rel))
	assert.Equal(t, filepath.Join(dir, ContextFileName), filepath.Join(dir, byKind(agent.ProbeKindContext).Rel))

	// claude reads CLAUDE.md and NOT AGENTS.md — the divergence from codex
	// that makes this contract worth having.
	for _, p := range cli.Probes {
		assert.NotEqual(t, "AGENTS.md", p.Rel, "claude does not read AGENTS.md")
	}
}

// TestEngineCLI_OneshotRequiresPrint pins claude's oneshot DISCRIMINATOR. A
// driver that stopped emitting --print while still piping the prompt on stdin
// hangs the real binary on its terminal handshake; against a name-only grammar
// the stand-in produced an identical, fully green report.
func TestEngineCLI_OneshotRequiresPrint(t *testing.T) {
	oneshot, ok := agent.EngineCLIFor(ClaudeEngineCLIs(), agent.CLISurfaceOneshot)
	require.True(t, ok)
	require.NoError(t, oneshot.Validate())

	_, err := oneshot.ParseArgv([]string{"--model", "sonnet"})
	var missing *agent.MissingFlagError
	assert.True(t, errors.As(err, &missing), "an oneshot line without --print must not parse: %v", err)

	_, err = oneshot.ParseArgv([]string{"--print", "--model", "sonnet"})
	assert.NoError(t, err)
}
