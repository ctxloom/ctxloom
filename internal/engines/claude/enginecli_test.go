package claude

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// argvCase is one point in the buildArgs matrix, carrying a label the failure
// output can name.
// matrixModel is the model every matrix case runs with. It is a single
// constant because the minimal posture pins the model into its --settings JSON
// at SETUP time while --model is emitted at Execute time: a matrix that let the
// two drift would be testing a request no caller can make.
const matrixModel = "claude-opus-4-8"

type argvCase struct {
	label   string
	surface agent.CLISurface
	req     *agent.ExecuteRequest
	// resume, when set, is the native key the Instance continues: the
	// --resume arm of the structured surface.
	resume string
}

// argvFor builds this case's argv. A resumed case binds the Instance,
// resumes it and reads Exec — the same composition buildArgs projects a
// request onto.
func (c argvCase) argvFor(t *testing.T, b *ClaudeCode) []string {
	t.Helper()
	if c.resume == "" {
		return b.buildArgs(c.req)
	}
	inst, err := b.kind.Instance(b.session(c.req))
	require.NoError(t, err)
	require.NoError(t, inst.Resume(c.resume))
	ex, err := inst.Exec(c.req.Presented)
	require.NoError(t, err)
	return ex.Args
}

// buildArgsMatrix enumerates EVERY argv shape the driver can produce —
// permission posture × mode × resume — with a harp in the env (so the
// interactive --name arm fires), a prompt (so the positional arm fires) and
// the runner-delivered presentations (so every out-of-cwd flag fires).
// Modulo the opaque ClaudeConfig.Args passthrough, which is user-supplied
// and undeclarable by construction (left empty here).
func buildArgsMatrix(presented []present.Presentation) []argvCase {
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
			for _, resume := range []string{"", "native-key"} {
				if resume != "" && mode.m != agent.ModeOneshot {
					continue // an interactive launch is never resumed by native key
				}
				// The session the runner binds the request to, with the
				// MCP servers the launch composed (the plan posture's
				// --allowedTools grants ride them).
				session := &engine.Session{
					Identity:   sessions.Identity{Harp: "perky-same-chevy"},
					Label:      engine.LabelConfig{Label: EngineName, Model: matrixModel},
					Mode:       engine.Mode(mode.m),
					Permission: engine.PermissionPolicy{Mode: perm.p},
					Prompt:     "do the thing",
					MCPServers: []string{"probe"},
				}
				out = append(out, argvCase{
					label:   fmt.Sprintf("%s/%s/resume=%q", perm.name, mode.name, resume),
					surface: mode.surface,
					resume:  resume,
					req: &agent.ExecuteRequest{
						Mode:        mode.m,
						Permissions: perm.p,
						Model:       matrixModel,
						Env:         map[string]string{sessionHarpEnv: "perky-same-chevy"},
						Prompt:      &agent.Fragment{Content: "do the thing"},
						Presented:   presented,
						Session:     session,
					},
				})
			}
		}
	}
	return out
}

// matrixPresentations is what the runner hands Execute after its static
// writer delivered the launch's package: the three out-of-cwd flag-carrying
// surfaces (--append-system-prompt-file, --mcp-config, --settings), each
// naming a real file, so every flag actually appears in the matrix's argv.
// Without this the delivered arm of buildArgs emits nothing and the test
// would silently cover less than it claims.
func matrixPresentations(t *testing.T) []present.Presentation {
	t.Helper()
	home := t.TempDir()
	flag := func(name, file, body string) present.Presentation {
		path := filepath.Join(home, file)
		require.NoError(t, os.WriteFile(path, []byte(body), 0o600))
		return present.Presentation{HostPath: path, EnginePath: path, Args: []string{name, path}}
	}
	return []present.Presentation{
		flag(flagAppendSystemFile, "abc123.sysprompt.md", "project rules"),
		flag(flagMCPConfig, ".mcp.json", `{"mcpServers":{"demo":{"command":"demo-server"}}}`),
		flag(flagSettings, "settings.json", `{"hooks":{}}`),
	}
}

// TestEngineCLI_BuildArgsFlagsAreDeclared is the ANTI-DRIFT GATE. Every flag
// buildArgs can emit, across the full permission × mode × resume matrix,
// must parse against the declared engine CLI grammar. A flag added to
// the driver without a declaration fails HERE, at the driver, instead of
// silently going missing from a stand-in binary that would keep reporting green.
//
// This is a real gate, not a coincidental pass: the assertion runs the driver's
// own output through agent.EngineCLI.ParseArgv, which errors on any token that
// starts with "-" and is not declared.
func TestEngineCLI_BuildArgsFlagsAreDeclared(t *testing.T) {
	b := NewClaudeCode()
	clis := b.EngineCLIs()

	for _, c := range buildArgsMatrix(matrixPresentations(t)) {
		t.Run(c.label, func(t *testing.T) {
			cli, ok := agent.EngineCLIFor(clis, c.surface)
			require.True(t, ok, "no declaration for surface %s", c.surface)
			require.NoError(t, cli.Validate())

			args := c.argvFor(t, b)
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
	b := NewClaudeCode()
	clis := b.EngineCLIs()

	emitted := map[agent.CLISurface]map[string]bool{}
	for _, c := range buildArgsMatrix(matrixPresentations(t)) {
		if emitted[c.surface] == nil {
			emitted[c.surface] = map[string]bool{}
		}
		for _, a := range c.argvFor(t, b) {
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

// TestEngineCLI_SettingsValueIsTheDeliveredPath: --settings names the file
// the runner delivered under the session home — an absolute path, read off
// the delivered presentation.
func TestEngineCLI_SettingsValueIsTheDeliveredPath(t *testing.T) {
	b := NewClaudeCode()
	clis := b.EngineCLIs()
	oneshot, _ := agent.EngineCLIFor(clis, agent.CLISurfaceOneshot)

	f, ok := oneshot.LookupFlag(flagSettings)
	require.True(t, ok)
	assert.Equal(t, agent.ValuePath, f.Value)

	args := b.buildArgs(&agent.ExecuteRequest{Mode: agent.ModeOneshot, Presented: matrixPresentations(t)})
	parsed, err := oneshot.ParseArgv(args)
	require.NoError(t, err)
	v, ok := parsed.Value(flagSettings)
	require.True(t, ok)
	assert.True(t, filepath.IsAbs(v), "delivery passes a file PATH, got %q", v)
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

	// claude reads CLAUDE.md and NOT AGENTS.md — the fact that makes this
	// contract worth having.
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
