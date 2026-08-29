//go:build parked_engines

package codex

import (
	"errors"
	"fmt"
	"path/filepath"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
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

// buildArgsMatrix enumerates EVERY combination codex's buildArgs branches on —
// permission posture × mode × SkipSetup — with a model (so the --model arm
// fires) and a prompt (so the positional arm fires). CellKind is absent on
// purpose and its absence is asserted below: unlike claude, codex's buildArgs
// never reads it, because codex has NO out-of-cwd redirect flag for any surface
// (surfaces.go's capability recap), so no cell kind can change its argv. A
// matrix carrying a CellKind axis here would claim coverage it does not have.
//
// The user-supplied CodexConfig.Args passthrough is undeclarable by
// construction (it is opaque), so it is left empty exactly as claude's matrix
// leaves ClaudeConfig.Args empty.
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
	var out []argvCase
	for _, perm := range perms {
		for _, mode := range modes {
			for _, skip := range []bool{false, true} {
				for _, model := range []string{"", "gpt-5-codex"} {
					out = append(out, argvCase{
						label:   fmt.Sprintf("%s/%s/skipSetup=%v/model=%q", perm.name, mode.name, skip, model),
						surface: mode.surface,
						req: &agent.ExecuteRequest{
							Mode:        mode.m,
							Permissions: perm.p,
							SkipSetup:   skip,
							Model:       model,
							Prompt:      &agent.Fragment{Content: "do the thing"},
						},
					})
				}
			}
		}
	}
	return out
}

// TestEngineCLI_BuildArgsFlagsAreDeclared is the ANTI-DRIFT GATE. Every flag
// buildArgs can emit, across the full permission × mode × SkipSetup × model
// matrix, must parse against the declared engine CLI grammar — including the
// leading `exec` subcommand, which ParseArgv requires at argv[0] on the oneshot
// surface.
//
// This is a real gate, not a coincidental pass: the assertion runs the driver's
// own output through agent.EngineCLI.ParseArgv, which errors on any token that
// starts with "-" and is not declared, AND buildArgs emits the flag CONSTANTS
// this declaration is built from — so a typo cannot make both sides agree on a
// spelling the vendor never sees. (internal/acp/argv.go's equivalent test does
// NOT have that property: its chatArgv still emits string literals.)
func TestEngineCLI_BuildArgsFlagsAreDeclared(t *testing.T) {
	b := NewCodex()
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
// declaration that outgrew the driver is dead weight a fake would honour and
// nothing would ever produce. Every declared flag (bar those marked Ignored,
// which exist only for vendor-grammar fidelity) must appear somewhere in the
// matrix, on the surface that declares it.
func TestEngineCLI_EveryDeclaredFlagIsEmitted(t *testing.T) {
	b := NewCodex()
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

// TestEngineCLI_ExecSubcommandIsOneshotOnly pins codex's structural divergence
// from claude: the oneshot surface is a distinct `codex exec` SUBCOMMAND (with
// its own flag set), the interactive surface is the bare binary. ParseArgv
// enforces it in BOTH directions, which is what a stand-in binary needs — it is
// spawned with one argv and must know which grammar it is being held to.
func TestEngineCLI_ExecSubcommandIsOneshotOnly(t *testing.T) {
	b := NewCodex()
	clis := b.EngineCLIs()
	oneshot, ok := agent.EngineCLIFor(clis, agent.CLISurfaceOneshot)
	require.True(t, ok)
	interactive, ok := agent.EngineCLIFor(clis, agent.CLISurfaceInteractive)
	require.True(t, ok)

	assert.Equal(t, subcommandExec, oneshot.Subcommand)
	assert.Empty(t, interactive.Subcommand, "the interactive TUI is the bare binary")

	oArgs := b.buildArgs(&agent.ExecuteRequest{Mode: agent.ModeOneshot})
	require.NotEmpty(t, oArgs)
	assert.Equal(t, subcommandExec, oArgs[0], "the driver puts exec first, before config passthrough args")
	parsed, err := oneshot.ParseArgv(oArgs)
	require.NoError(t, err)
	assert.Equal(t, subcommandExec, parsed.Subcommand)

	// The oneshot line must NOT parse as an interactive one: `exec` reads as a
	// stray positional there, which for a PromptPositional surface would be read
	// as the prompt — exactly the misreading a shared grammar has to prevent.
	iParsed, err := interactive.ParseArgv(oArgs)
	require.NoError(t, err)
	assert.Contains(t, iParsed.Positionals, subcommandExec)

	// And the interactive line must not satisfy the oneshot grammar at all.
	iArgs := b.buildArgs(&agent.ExecuteRequest{Mode: agent.ModeInteractive})
	_, err = oneshot.ParseArgv(iArgs)
	var subErr *agent.SubcommandError
	require.ErrorAs(t, err, &subErr)
	assert.Equal(t, subcommandExec, subErr.Want)
}

// TestEngineCLI_AskForApprovalIsInteractiveOnly pins the flag whose
// surface-scoping IS codex's real exit-2 behaviour: `codex exec` REJECTS
// --ask-for-approval outright ("unexpected argument"), because a
// non-interactive run has nobody to ask. Declaring it on the interactive
// surface only makes ParseArgv reproduce that rejection, which is why the
// draft's separate `Rejected bool` field was redundant.
func TestEngineCLI_AskForApprovalIsInteractiveOnly(t *testing.T) {
	b := NewCodex()
	clis := b.EngineCLIs()
	oneshot, _ := agent.EngineCLIFor(clis, agent.CLISurfaceOneshot)
	interactive, _ := agent.EngineCLIFor(clis, agent.CLISurfaceInteractive)

	_, onOneshot := oneshot.LookupFlag(flagAskForApproval)
	assert.False(t, onOneshot, "codex exec rejects %s with exit 2", flagAskForApproval)
	_, onInteractive := interactive.LookupFlag(flagAskForApproval)
	assert.True(t, onInteractive, "the TUI accepts %s", flagAskForApproval)

	// The driver agrees: a never-approve posture emits it interactively and
	// suppresses it on oneshot.
	plan := func(mode agent.ExecutionMode) []string {
		return b.buildArgs(&agent.ExecuteRequest{Mode: mode, Permissions: agent.PermissionPlan})
	}
	iParsed, err := interactive.ParseArgv(plan(agent.ModeInteractive))
	require.NoError(t, err)
	v, ok := iParsed.Value(flagAskForApproval)
	require.True(t, ok, "plan posture asks codex never to approve")
	assert.Equal(t, "never", v)

	oParsed, err := oneshot.ParseArgv(plan(agent.ModeOneshot))
	require.NoError(t, err)
	assert.False(t, oParsed.Has(flagAskForApproval), "oneshot must never emit a flag codex exec exits 2 on")
}

// TestEngineCLI_SandboxAndBypassAreMutuallyExclusive pins the sandbox tier rule
// that stays in buildArgs as CODE while the declaration only says both flags
// exist: bypass carries the full-access escape hatch and NO --sandbox; every
// other posture NAMES its tier rather than inheriting codex's default.
func TestEngineCLI_SandboxAndBypassAreMutuallyExclusive(t *testing.T) {
	b := NewCodex()
	clis := b.EngineCLIs()
	oneshot, _ := agent.EngineCLIFor(clis, agent.CLISurfaceOneshot)

	for _, tc := range []struct {
		name    string
		req     *agent.ExecuteRequest
		sandbox string
	}{
		{"default", &agent.ExecuteRequest{Mode: agent.ModeOneshot}, "workspace-write"},
		{"acceptEdits", &agent.ExecuteRequest{Mode: agent.ModeOneshot, Permissions: agent.PermissionAcceptEdits}, "workspace-write"},
		{"plan", &agent.ExecuteRequest{Mode: agent.ModeOneshot, Permissions: agent.PermissionPlan}, "read-only"},
		{"skipSetup", &agent.ExecuteRequest{Mode: agent.ModeOneshot, SkipSetup: true}, "read-only"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			parsed, err := oneshot.ParseArgv(b.buildArgs(tc.req))
			require.NoError(t, err)
			v, ok := parsed.Value(flagSandbox)
			require.True(t, ok, "every non-bypass posture names its sandbox tier")
			assert.Equal(t, tc.sandbox, v)
			assert.False(t, parsed.Has(flagBypassApprovalsAndSandbox))
		})
	}

	bypass, err := oneshot.ParseArgv(b.buildArgs(&agent.ExecuteRequest{Mode: agent.ModeOneshot, Permissions: agent.PermissionBypass}))
	require.NoError(t, err)
	assert.True(t, bypass.Has(flagBypassApprovalsAndSandbox))
	assert.False(t, bypass.Has(flagSandbox), "bypass must not also name a sandbox tier — that would collapse two postures into one")
}

// TestEngineCLI_PromptDeliveryMatchesDriver pins the divergence from claude that
// a naive fake would smooth away: codex takes the prompt as an argv POSITIONAL
// on BOTH surfaces, where claude's oneshot pipes it on STDIN. A fake sharing
// claude's assumption would report "no prompt received" from a codex run that
// delivered one perfectly.
func TestEngineCLI_PromptDeliveryMatchesDriver(t *testing.T) {
	b := NewCodex()
	clis := b.EngineCLIs()
	const task = "review this enormous diff"
	prompt := &agent.Fragment{Content: task}

	for _, tc := range []struct {
		surface agent.CLISurface
		mode    agent.ExecutionMode
	}{
		{agent.CLISurfaceOneshot, agent.ModeOneshot},
		{agent.CLISurfaceInteractive, agent.ModeInteractive},
	} {
		t.Run(string(tc.surface), func(t *testing.T) {
			cli, ok := agent.EngineCLIFor(clis, tc.surface)
			require.True(t, ok)
			assert.Equal(t, agent.PromptPositional, cli.Prompt)

			parsed, err := cli.ParseArgv(b.buildArgs(&agent.ExecuteRequest{Mode: tc.mode, Prompt: prompt}))
			require.NoError(t, err)
			require.NotEmpty(t, parsed.Positionals, "codex carries the prompt on argv, not stdin")
			assert.Equal(t, task, parsed.Positionals[len(parsed.Positionals)-1])
		})
	}

	// And ExecuteCLI is handed a nil oneshot stdin reader — the positional IS
	// the delivery, there is no second channel.
	assert.Equal(t, agent.PromptPositional, mustCLI(t, clis, agent.CLISurfaceOneshot).Prompt)
}

// TestEngineCLI_ProbesMatchTheWriters pins the declared probe paths to the paths
// ctxloom's writers actually target, so the declaration cannot describe a file
// nothing writes. codex's probes resolve against TWO different roots — the cwd
// (AGENTS.md, the context cache) and CODEX_HOME (config.toml, prompts, skills).
//
// The env-dir probes are anchored on a RESOLVED codex home, which is the only
// kind that exists since S7: dir here stands for the run path's already-resolved
// virtual project dir (a per-session instance, a worktree home, a container's
// $HOME), and cellScopedCodexHome(dir) is the CODEX_HOME ctxloom exports for it.
// There is deliberately no project-root spelling to anchor on — see
// declared_absence.go.
func TestEngineCLI_ProbesMatchTheWriters(t *testing.T) {
	clis := CodexEngineCLIs()
	cli := mustCLI(t, clis, agent.CLISurfaceInteractive)
	require.NoError(t, cli.Validate())

	const dir = "/work"
	home := cellScopedCodexHome(dir)
	w := &CodexHookWriter{}

	byKindScope := func(kind agent.ProbeKind, scope agent.ProbeScope) agent.CLIProbe {
		for _, p := range cli.ProbesFor(kind) {
			if p.Scope == scope {
				return p
			}
		}
		t.Fatalf("no %s probe declared for %s", scope, kind)
		return agent.CLIProbe{}
	}

	// CODEX_HOME-rooted surfaces, against the writers that target them.
	settings := byKindScope(agent.ProbeKindSettings, agent.ScopeEnvDir)
	assert.Equal(t, CodexHomeEnv, settings.EnvVar)
	assert.Equal(t, ConfigDirName, settings.EnvHomeDefault)
	assert.Equal(t, w.settingsPathIn(dir), filepath.Join(home, settings.Rel))

	commands := byKindScope(agent.ProbeKindCommands, agent.ScopeEnvDir)
	assert.True(t, commands.Dir)
	assert.Equal(t, cellScopedPromptsDir(dir), filepath.Join(home, commands.Rel))

	skills := byKindScope(agent.ProbeKindSkills, agent.ScopeEnvDir)
	assert.True(t, skills.Dir)
	assert.Equal(t, cellScopedSkillsDir(dir), filepath.Join(home, skills.Rel))

	// cwd-rooted context: AGENTS.md is the native read.
	contexts := cli.ProbesFor(agent.ProbeKindContext)
	require.Len(t, contexts, 2, "codex has two coexisting context routes")
	assert.Equal(t, AgentsMDFile, contexts[0].Rel)
	assert.Equal(t, filepath.Join(dir, AgentsMDFile), filepath.Join(dir, contexts[0].Rel))

	// codex reads AGENTS.md and NOT CLAUDE.md — the divergence from claude that
	// makes this contract worth having.
	for _, p := range cli.Probes {
		assert.NotEqual(t, "CLAUDE.md", p.Rel, "codex does not read CLAUDE.md")
	}
}

// TestEngineCLI_ContextIsHookMediated pins THE decision this declaration had to
// make. codex's run-path context is a per-run content-hash file under
// .ctxloom/cache/context/ that codex NEVER opens: config.toml's [hooks]
// SessionStart command has the hash baked into its argv and codex ingests the
// hook's output. So the probe is a DIRECTORY (the filename is per-run, there is
// no fixed Rel) carrying a note that names the mediator — rather than a new
// `Via ProbeKind` field on the shared contract, which would have had exactly one
// user.
//
// The directory is asserted against agent.SCMContextSubdir, the same constant
// agent.WriteContextFile writes into and contextSurface.Deliver removes from, so
// the probe cannot drift from the writer.
func TestEngineCLI_ContextIsHookMediated(t *testing.T) {
	cli := mustCLI(t, CodexEngineCLIs(), agent.CLISurfaceOneshot)
	contexts := cli.ProbesFor(agent.ProbeKindContext)
	require.Len(t, contexts, 2)

	hook := contexts[1]
	assert.Equal(t, agent.ScopeCwd, hook.Scope)
	assert.Equal(t, agent.SCMContextSubdir, hook.Rel, "the writer's directory, not a fabricated one")
	assert.True(t, hook.Dir, "the FILENAME is per-run (a content hash), so only the directory is declarable")
	assert.NotEmpty(t, hook.Note, "an indirect read is undeclarable without saying so")

	// The mediator is a real, declared surface of this same engine — config.toml
	// under CODEX_HOME. If that probe ever disappeared, the note above would be
	// describing a file ctxloom no longer writes.
	require.NotEmpty(t, cli.ProbesFor(agent.ProbeKindSettings))
}

// TestEngineCLI_MCPFoldsIntoSettings pins the fold: codex has ONE config.toml
// carrying both [hooks] and [mcp_servers], so ProbesFor(ProbeKindMCP) is
// legitimately EMPTY. An empty result here is a DECLARED absence, not a missing
// declaration — the same fold Surfaces.SupportedApproaches reports by omitting
// SurfaceMCP.
func TestEngineCLI_MCPFoldsIntoSettings(t *testing.T) {
	for _, cli := range CodexEngineCLIs() {
		assert.Empty(t, cli.ProbesFor(agent.ProbeKindMCP),
			"%s: codex folds MCP into config.toml; a separate MCP probe would describe a file that does not exist", cli.Surface)
		require.Len(t, cli.ProbesFor(agent.ProbeKindSettings), 1)
		assert.Empty(t, Surfaces{}.SupportedApproaches(agent.SurfaceMCP),
			"the surface table folds MCP the same way, so the two cannot disagree")
	}
}

// TestEngineCLI_NoOutOfCwdRedirect pins the capability that shapes every other
// decision here: codex exposes NO per-invocation redirect for ANY surface, so
// no probe can be ScopeFlagValue and no flag carries a path. That is why
// concurrent per-agent isolation for codex requires a private cwd (worktree) or
// a container, and why this test needs no Setup to populate delivery paths the
// way claude's matrix does.
func TestEngineCLI_NoOutOfCwdRedirect(t *testing.T) {
	for _, cli := range CodexEngineCLIs() {
		for _, p := range cli.Probes {
			assert.NotEqual(t, agent.ScopeFlagValue, p.Scope,
				"%s: codex has no --mcp-config/--settings/--append-system-prompt-file equivalent", cli.Surface)
		}
		for _, f := range cli.Flags {
			assert.NotEqual(t, agent.ValuePath, f.Value, "%s: no codex flag carries a path", cli.Surface)
		}
	}
}

// TestEngineCLI_SetEnvDeclaresCodexHome pins codex's structural divergence on
// the env axis: it is the ONE backend with a SetExecuteEnv contributor, because
// its config/prompts/skills are CODEX_HOME-relative rather than cwd-relative.
// The declaration and cellCodexHomeEnv are asserted against each other so
// neither can move alone.
func TestEngineCLI_SetEnvDeclaresCodexHome(t *testing.T) {
	b := NewCodex()
	b.resolvedProjectDir = "/work"
	env := b.cellCodexHomeEnv(&agent.ExecuteRequest{WorkDir: "/work"})
	require.Contains(t, env, CodexHomeEnv)
	assert.Equal(t, cellScopedCodexHome("/work"), env[CodexHomeEnv])

	for _, cli := range CodexEngineCLIs() {
		assert.Contains(t, cli.SetEnv, CodexHomeEnv, "%s", cli.Surface)
		assert.Contains(t, cli.SetEnv, agent.SCMContextFileEnv, "%s", cli.Surface)
		assert.Empty(t, cli.StripEnv, "%s: codex's CLI surfaces strip nothing", cli.Surface)
	}
}

// mustCLI selects a declared surface or fails the test.
func mustCLI(t *testing.T, clis []agent.EngineCLI, surface agent.CLISurface) agent.EngineCLI {
	t.Helper()
	cli, ok := agent.EngineCLIFor(clis, surface)
	require.True(t, ok, "no declaration for surface %s", surface)
	return cli
}

// TestEngineCLI_DeclaresCodexArgvConstraints pins the two whole-line rules
// codex's own help states and a name-only grammar could not enforce: --sandbox
// takes one of three tiers, and the bypass escape hatch may not accompany it.
// Without these the mock accepts `--sandbox nonsense` and both flags together
// — argv lines the real binary exits 2 on — and reports a green run for a
// launch that never started.
func TestEngineCLI_DeclaresCodexArgvConstraints(t *testing.T) {
	oneshot, ok := agent.EngineCLIFor(CodexEngineCLIs(), agent.CLISurfaceOneshot)
	require.True(t, ok)
	require.NoError(t, oneshot.Validate())

	_, err := oneshot.ParseArgv([]string{"exec", "--sandbox", "nonsense"})
	var badVal *agent.ValueNotAllowedError
	assert.True(t, errors.As(err, &badVal), "an undeclared sandbox tier must not parse: %v", err)

	_, err = oneshot.ParseArgv([]string{"exec", "--sandbox", "read-only", "--dangerously-bypass-approvals-and-sandbox"})
	var conflict *agent.ConflictingFlagsError
	assert.True(t, errors.As(err, &conflict), "the bypass flag must not parse alongside --sandbox: %v", err)

	_, err = oneshot.ParseArgv([]string{"exec", "--sandbox", "workspace-write"})
	assert.NoError(t, err, "a declared tier on its own is legal")
}
