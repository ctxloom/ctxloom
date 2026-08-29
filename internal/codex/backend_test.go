package codex

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Capability wiring — codex is a full LaunchBackend agent.
// =============================================================================

func TestCodex_Capabilities(t *testing.T) {
	codex := NewCodex()
	assert.Equal(t, "codex", codex.Name())
	// codex's SessionHistory scraper was deleted outright (it had an
	// envelope-vs-flat parse mismatch); History() is nil now that canonical
	// capture is the only transcript source for codex.
	assert.Nil(t, codex.History(), "session history scraper retired, tough-cloud S5")
}

func TestCodex_Configure(t *testing.T) {
	codex := NewCodex()
	codex.Configure(&CodexConfig{
		BinaryPath: "/custom/codex",
		Args:       []string{"--foo"},
		Env:        map[string]string{"K": "V"},
	})
	assert.Equal(t, "/custom/codex", codex.BinaryPath)
	assert.Equal(t, []string{"--foo"}, codex.Args)
	assert.Equal(t, "V", codex.Env["K"])
}

// foreignBackendConfig is a config for some OTHER backend — the only input for
// which Configure's type assert can miss.
type foreignBackendConfig struct{}

func (foreignBackendConfig) BackendType() string { return "not-codex" }

// A past review called Configure's missing else a silent swallow. The branch is
// unreachable by construction, not merely untaken: every caller resolves the
// config BY the backend's own name — backends.ConfiguredBackend does
// Get(cfg.BackendType()), llm_serve's serveBackendConfig gates on
// entry.EffectiveType() == backendName, and acp.NewChatDriver configures a
// backend with its own config type. A config that reached codex's Configure at
// all therefore declares BackendType "codex", which only *CodexConfig does.
// This pins both halves of that: the real config applies every field it
// declares, and the assert's guard string is codex's own.
func TestCodex_Configure_MismatchIsUnreachableNotSwallowed(t *testing.T) {
	assert.Equal(t, "codex", CodexConfig{}.BackendType())
	assert.NotEqual(t, CodexConfig{}.BackendType(), foreignBackendConfig{}.BackendType(),
		"a foreign config never routes here: the registry dispatches on this string")

	b := NewCodex()
	b.Configure(&foreignBackendConfig{})
	assert.Equal(t, "codex", b.BinaryPath, "an unreachable input leaves the backend at its defaults")

	b.Configure(&CodexConfig{BinaryPath: "/custom/codex", Args: []string{"--x"}, Env: map[string]string{"K": "V"}, Thinking: "high"})
	assert.Equal(t, "/custom/codex", b.BinaryPath)
	assert.Equal(t, []string{"--x"}, b.Args)
	assert.Equal(t, "V", b.Env["K"])
	assert.Equal(t, agent.ThinkingHigh, b.thinking)
}

// TestNewCodex_ThinkingDefaultsToMedium pins that a freshly-constructed
// backend that never runs Configure at all still resolves to the documented
// medium default (agent.ThinkingLevel's zero value IS ThinkingMedium).
func TestNewCodex_ThinkingDefaultsToMedium(t *testing.T) {
	codex := NewCodex()
	assert.Equal(t, agent.ThinkingMedium, codex.thinking)
}

// TestCodex_Configure_Thinking verifies the normalized thinking level parses
// through Configure onto the backend.
func TestCodex_Configure_Thinking(t *testing.T) {
	codex := NewCodex()
	codex.Configure(&CodexConfig{Thinking: "low"})
	assert.Equal(t, agent.ThinkingLow, codex.thinking)
}

// TestCodex_Configure_ThinkingUnknownWarnsAndDefaults verifies an
// unrecognized (but non-empty) value still resolves to medium AND warns.
func TestCodex_Configure_ThinkingUnknownWarnsAndDefaults(t *testing.T) {
	r, w, err := os.Pipe()
	assert.NoError(t, err)
	orig := os.Stderr
	os.Stderr = w

	codex := NewCodex()
	codex.Configure(&CodexConfig{Thinking: "extreme"})
	_ = w.Close()
	os.Stderr = orig

	out, err := io.ReadAll(r)
	assert.NoError(t, err)
	assert.Equal(t, agent.ThinkingMedium, codex.thinking)
	assert.Contains(t, string(out), "extreme")
}

// =============================================================================
// buildArgs — context now reaches codex via the SessionStart hook + context
// file, so buildArgs no longer prepends context to the prompt.
// =============================================================================

func TestCodex_buildArgs_InteractiveBasic(t *testing.T) {
	codex := NewCodex()
	codex.Args = []string{"--model", "gpt-4"}

	req := &agent.ExecuteRequest{Mode: agent.ModeInteractive, Prompt: &agent.Fragment{Content: "test prompt"}}
	args := codex.buildArgs(req)

	assert.NotContains(t, args, "exec", "interactive runs do not use the exec subcommand")
	assert.Contains(t, args, "--model")
	assert.Contains(t, args, "gpt-4")
	assert.Contains(t, args, "test prompt")
}

func TestCodex_buildArgs_OneshotUsesExec(t *testing.T) {
	codex := NewCodex()
	req := &agent.ExecuteRequest{Mode: agent.ModeOneshot, Prompt: &agent.Fragment{Content: "summarize"}}
	args := codex.buildArgs(req)

	assert.Equal(t, "exec", args[0], "oneshot runs use the non-interactive exec subcommand")
	assert.Equal(t, "summarize", args[len(args)-1])
}

// TestCodex_buildArgs_PostureMatrix pins the argv codex actually receives for every
// posture on BOTH subcommands. The two subcommands take DIFFERENT flags and reject
// a foreign one with exit 2 ("unexpected argument"), so each posture is asserted
// per-mode: --ask-for-approval is interactive-only, and --full-auto exists on
// neither (verified against codex-cli 0.144.4).
func TestCodex_buildArgs_PostureMatrix(t *testing.T) {
	tests := []struct {
		name     string
		req      agent.ExecuteRequest
		wants    []string
		notWants []string
	}{
		{
			name:     "default oneshot states workspace-write",
			req:      agent.ExecuteRequest{Mode: agent.ModeOneshot, Permissions: agent.PermissionDefault},
			wants:    []string{"exec", "--sandbox", "workspace-write"},
			notWants: []string{"--ask-for-approval", "--full-auto", "read-only"},
		},
		{
			name:     "default interactive states workspace-write",
			req:      agent.ExecuteRequest{Mode: agent.ModeInteractive, Permissions: agent.PermissionDefault},
			wants:    []string{"--sandbox", "workspace-write"},
			notWants: []string{"exec", "--ask-for-approval", "--full-auto"},
		},
		{
			name:     "acceptEdits has no codex tier of its own and follows default",
			req:      agent.ExecuteRequest{Mode: agent.ModeOneshot, Permissions: agent.PermissionAcceptEdits},
			wants:    []string{"--sandbox", "workspace-write"},
			notWants: []string{"--ask-for-approval", "--full-auto"},
		},
		{
			name:  "plan oneshot is read-only and never names the interactive-only approval flag",
			req:   agent.ExecuteRequest{Mode: agent.ModeOneshot, Permissions: agent.PermissionPlan},
			wants: []string{"exec", "--sandbox", "read-only"},
			// `codex exec --ask-for-approval` is an exit-2 parse error: it kills the run.
			notWants: []string{"--ask-for-approval", "--full-auto", "workspace-write"},
		},
		{
			name:     "plan interactive is read-only and never prompts",
			req:      agent.ExecuteRequest{Mode: agent.ModeInteractive, Permissions: agent.PermissionPlan},
			wants:    []string{"--sandbox", "read-only", "--ask-for-approval", "never"},
			notWants: []string{"exec", "--full-auto", "workspace-write"},
		},
		{
			name:     "bypass oneshot uses codex's full-access escape hatch",
			req:      agent.ExecuteRequest{Mode: agent.ModeOneshot, Permissions: agent.PermissionBypass},
			wants:    []string{"exec", "--dangerously-bypass-approvals-and-sandbox"},
			notWants: []string{"--full-auto", "--sandbox", "--ask-for-approval"},
		},
		{
			name: "bypass interactive uses codex's full-access escape hatch",
			req:  agent.ExecuteRequest{Mode: agent.ModeInteractive, Permissions: agent.PermissionBypass},
			// `codex --full-auto` is an exit-2 parse error: it kills the most common posture.
			wants:    []string{"--dangerously-bypass-approvals-and-sandbox"},
			notWants: []string{"exec", "--full-auto", "--sandbox", "--ask-for-approval"},
		},
		{
			name:     "SkipSetup oneshot (distill/compaction) is read-only, no approval flag",
			req:      agent.ExecuteRequest{Mode: agent.ModeOneshot, SkipSetup: true},
			wants:    []string{"exec", "--sandbox", "read-only"},
			notWants: []string{"--ask-for-approval", "--full-auto", "workspace-write"},
		},
		{
			name:     "SkipSetup outranks a bypass posture",
			req:      agent.ExecuteRequest{Mode: agent.ModeOneshot, SkipSetup: true, Permissions: agent.PermissionBypass},
			wants:    []string{"--sandbox", "read-only"},
			notWants: []string{"--dangerously-bypass-approvals-and-sandbox", "--full-auto"},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			req := tc.req
			req.Prompt = &agent.Fragment{Content: "x"}
			args := NewCodex().buildArgs(&req)

			assert.Subset(t, args, tc.wants, "argv %v", args)
			for _, flag := range tc.notWants {
				assert.NotContains(t, args, flag, "argv %v", args)
			}
			assert.Equal(t, "x", args[len(args)-1], "prompt is the trailing positional")
		})
	}
}

func TestCodex_buildArgs_EmptyPrompt(t *testing.T) {
	codex := NewCodex()
	codex.Args = []string{"--model", "gpt-4"}
	req := &agent.ExecuteRequest{Mode: agent.ModeInteractive, Prompt: &agent.Fragment{Content: ""}}
	assert.Equal(t, []string{"--model", "gpt-4", "--sandbox", "workspace-write"}, codex.buildArgs(req))
}

func TestCodex_buildArgs_NilPrompt(t *testing.T) {
	codex := NewCodex()
	codex.Args = []string{"--model", "gpt-4"}
	req := &agent.ExecuteRequest{Mode: agent.ModeInteractive, Prompt: nil}
	assert.Equal(t, []string{"--model", "gpt-4", "--sandbox", "workspace-write"}, codex.buildArgs(req))
}

func TestCodex_buildArgs_PreservesBaseArgs(t *testing.T) {
	codex := NewCodex()
	codex.Args = []string{"--arg1", "--arg2"}
	req := &agent.ExecuteRequest{Mode: agent.ModeInteractive, Prompt: &agent.Fragment{Content: "test"}}

	_ = codex.buildArgs(req)
	args2 := codex.buildArgs(req)

	assert.Equal(t, "--arg1", args2[0])
	assert.Equal(t, "--arg2", args2[1])
	assert.Equal(t, []string{"--arg1", "--arg2"}, codex.Args, "base Args must not be mutated")
}

func TestCodex_buildArgs_Model(t *testing.T) {
	codex := NewCodex()
	req := &agent.ExecuteRequest{Model: "gpt-5-codex", Prompt: &agent.Fragment{Content: "x"}}
	args := codex.buildArgs(req)

	found := false
	for i, a := range args {
		if a == "--model" && i+1 < len(args) && args[i+1] == "gpt-5-codex" {
			found = true
		}
	}
	assert.True(t, found, "buildArgs passes --model <model> when requested")
}

func TestCodex_buildArgs_NoModelWhenEmpty(t *testing.T) {
	codex := NewCodex()
	req := &agent.ExecuteRequest{Prompt: &agent.Fragment{Content: "x"}}
	assert.NotContains(t, codex.buildArgs(req), "--model", "no --model when none requested (codex uses its default)")
}

// =============================================================================
// CODEX_HOME single ownership — resolveCodexProjectDir is the one function
// Setup's delivery target and Execute's env both read, so they can never
// disagree about where CODEX_HOME points. codex's credential seeding is now
// three-way (ensureCodexCredentials below): the isolation package's
// copy-based framework (credentialSeedSpecs["codex"]) for the worktree
// fan-out axis, plus codex's OWN active seed (in-tree host run) and verify
// (container fresh-$HOME) for the two axes the isolation package never sees.
// =============================================================================

// TestResolveCodexProjectDir_NoControlledHome_UsesTheRealHostHome pins the
// core case of the no-relocation ruling. With no isolation-provided CODEX_HOME and no
// container cell, codex uses the user's OWN ~/.codex — it no longer relocates
// unconditionally. It relocated for years on the reasoning that the target was
// durable and nothing of the user's was being taken; a per-session, disposable
// instance takes token refreshes, accumulated trust and session state away
// every session, which is why the decision now rides config_home like claude's
// and kiro's, in operations.InTreeAgentHomeEnv, and reaches this function only
// as an isolation-provided value.
func TestResolveCodexProjectDir_NoControlledHome_UsesTheRealHostHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir, source := resolveCodexProjectDir(nil, "/proj", agent.CellKindShared)
	assert.Equal(t, home, dir)
	assert.Equal(t, codexHomeRealHost, source)
	assert.Equal(t, filepath.Join(home, ConfigDirName), cellScopedCodexHome(dir),
		"the final CODEX_HOME is the user's real ~/.codex")
	assert.NotContains(t, dir, ".ctxloom",
		"no controlled home means no in-tree relocation at all — not even a per-session one")
}

// TestResolveCodexProjectDir_NoControlledHome_IgnoresWorkDir states the same
// fact from the other side: WorkDir no longer participates in the fallback, so
// no spelling of the project root (nor an empty one) can produce an in-tree
// codex home by accident.
func TestResolveCodexProjectDir_NoControlledHome_IgnoresWorkDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	for _, workDir := range []string{"", "/proj", "relative/proj"} {
		dir, source := resolveCodexProjectDir(nil, workDir, agent.CellKindShared)
		assert.Equal(t, home, dir, "WorkDir %q must not affect the real-home fallback", workDir)
		assert.Equal(t, codexHomeRealHost, source)
	}
}

// TestResolveCodexProjectDir_SessionInstance_ArrivesAsIsolationProvided is D2's
// other half: the per-session instance a `config_home: project` binding earns
// is contributed as CODEX_HOME by operations.InTreeAgentHomeEnv (through the
// backends inTreeAgentHome seam), so codex sees it as an already-prepared home
// and strips its ".codex" suffix back to the virtual project dir. There is ONE
// decision point, and it is not here.
func TestResolveCodexProjectDir_SessionInstance_ArrivesAsIsolationProvided(t *testing.T) {
	instance, err := SessionHome("/proj", "ugly-icy-squid")
	require.NoError(t, err)

	dir, source := resolveCodexProjectDir(
		map[string]string{CodexHomeEnv: filepath.Join(instance, ConfigDirName)}, "/proj", agent.CellKindShared)

	assert.Equal(t, instance, dir)
	assert.Equal(t, codexHomeIsolationProvided, source,
		"a ctxloom-created instance is already prepared and is trust-pre-seed eligible")
	assert.Equal(t, filepath.Join("/proj", ".ctxloom", "state", "ugly-icy-squid", "home", ".codex"), cellScopedCodexHome(dir),
		"spelled out once, so a change to the instance layout cannot pass by agreeing with itself")
}

// TestResolveCodexProjectDir_IsolationProvided_StripsCodexSuffix is the
// single-owner fix's core case: an isolation-provided CODEX_HOME (worktree's
// per-agent config-home, always ending in "/.codex" — credentialSeedSpecs's
// codex HomeVar Subdir) wins over WorkDir, and the ".codex" suffix is
// stripped back to the virtual project dir cellScopedCodexHome expects — so
// existing writers (SettingsPath, cellScopedPromptsDir/SkillsDir) resolve
// the SAME final home unchanged.
func TestResolveCodexProjectDir_IsolationProvided_StripsCodexSuffix(t *testing.T) {
	dir, source := resolveCodexProjectDir(map[string]string{"CODEX_HOME": "/tmp/ctxloom-cfg-x/.codex"}, "/proj", agent.CellKindShared)
	assert.Equal(t, "/tmp/ctxloom-cfg-x", dir)
	assert.Equal(t, codexHomeIsolationProvided, source, "an isolation-provided home is ephemeral — safe to pre-seed trust into")
	assert.Equal(t, "/tmp/ctxloom-cfg-x/.codex", cellScopedCodexHome(dir), "the final CODEX_HOME round-trips exactly")
}

// TestResolveCodexProjectDir_IsolationProvided_UnexpectedShape covers an
// isolation-provided CODEX_HOME that does NOT end in "/.codex" (a caller
// override) — used AS the project dir directly rather than dropped, so
// Setup and Execute still agree even on an unexpected shape.
func TestResolveCodexProjectDir_IsolationProvided_UnexpectedShape(t *testing.T) {
	var dir string
	var source codexHomeSource
	// The shape is announced (see _UnexpectedShape_IsAnnounced); capture the
	// warning so it does not ride this package's test output.
	captureStderr(t, func() {
		dir, source = resolveCodexProjectDir(map[string]string{"CODEX_HOME": "/custom/home"}, "/proj", agent.CellKindShared)
	})
	assert.Equal(t, "/custom/home", dir)
	assert.Equal(t, codexHomeIsolationProvided, source)
}

// TestResolveCodexProjectDir_ProcessIsolated_UsesHomeEnv pins the container
// core case: a container cell (no isolation-provided CODEX_HOME — the
// isolation package's Env() never sets one for a container workspace) lands
// on the container's OWN fresh $HOME instead of falling through to WorkDir
// (the bind-mounted PROJECT dir, where codexCredentialMounts never put
// anything) — so the resolved CODEX_HOME actually reaches the auth.json the
// isolation layer bind-mounted in.
func TestResolveCodexProjectDir_ProcessIsolated_UsesHomeEnv(t *testing.T) {
	t.Setenv("HOME", "/home/ctxloom")
	dir, source := resolveCodexProjectDir(nil, "/workspace/proj", agent.CellKindProcessIsolated)
	assert.Equal(t, "/home/ctxloom", dir)
	assert.Equal(t, codexHomeContainerFresh, source)
	assert.Equal(t, "/home/ctxloom/.codex", cellScopedCodexHome(dir), "matches codexCredentialMounts' mount target exactly")
}

// TestResolveCodexProjectDir_NoResolvableHome_ContributesNothing covers the
// one degenerate case: no $HOME at all (a container cell whose spec forgot one,
// or a host with an unresolvable home dir). The answer is to name NO home —
// contributing an invented path would point codex somewhere nobody asked for,
// which is worse than letting codex apply its own ~/.codex precedence and fail
// on its own terms. It is announced, never silent.
func TestResolveCodexProjectDir_NoResolvableHome_ContributesNothing(t *testing.T) {
	t.Setenv("HOME", "")
	var dir string
	var source codexHomeSource
	out := captureStderr(t, func() {
		dir, source = resolveCodexProjectDir(nil, "/workspace/proj", agent.CellKindProcessIsolated)
	})
	assert.Empty(t, dir, "no resolvable home means no CODEX_HOME contribution")
	assert.Equal(t, codexHomeRealHost, source)
	assert.Contains(t, out, CodexHomeEnv, "the degradation is announced")

	b := NewCodex()
	b.resolvedProjectDir = dir
	assert.Nil(t, b.cellCodexHomeEnv(&agent.ExecuteRequest{WorkDir: "/workspace/proj", Env: map[string]string{}}),
		"and Execute contributes no CODEX_HOME either, rather than a bare relative .codex")
}

// =============================================================================
// ensureCodexCredentials — the fail-loud gate added for both credential axes:
// Setup resolves it and stashes b.credentialErr; Execute refuses to launch
// codex at all when it is set (see the Execute tests further below).
// =============================================================================

// TestEnsureCodexCredentials_IsolationProvided_NoOp pins that an
// already-prepared home is a deliberate no-op here: the worktree fan-out axis
// is seeded (and fails loud) upstream by isolation.Prepare, and the per-session
// instance is prepared (and fails loud) by the inTreeAgentHome spec's Prepare,
// both before this run starts.
func TestEnsureCodexCredentials_IsolationProvided_NoOp(t *testing.T) {
	// A directory with NO auth.json: if this arm ever started verifying, this
	// would fail — which is the point, it must not.
	assert.NoError(t, ensureCodexCredentials(t.TempDir(), codexHomeIsolationProvided, resolveOpenAIAPIKey(nil, nil)))
}

// TestEnsureCodexCredentials_EnvTrigger_SkipsEverything pins OPENAI_API_KEY as
// codex's envTrigger: present, it bypasses verification on every axis —
// matching hostCredentialSeed's own envTrigger precedence.
func TestEnsureCodexCredentials_EnvTrigger_SkipsEverything(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-test")
	assert.NoError(t, ensureCodexCredentials(t.TempDir(), codexHomeRealHost, resolveOpenAIAPIKey(nil, nil)))
	assert.NoError(t, ensureCodexCredentials(t.TempDir(), codexHomeContainerFresh, resolveOpenAIAPIKey(nil, nil)))
}

// TestEnsureCodexCredentials_RealHost_VerifiesAndNeverWrites is the ruling's
// hardest line applied to codex's own Setup: the real ~/.codex is the user's,
// and ctxloom neither copies into it nor creates it. A missing auth.json fails
// LOUD with the user's own fix (`codex login`) rather than being papered over
// by a copy from somewhere else — and the directory is left byte-for-byte as it
// was found, including still absent.
func TestEnsureCodexCredentials_RealHost_VerifiesAndNeverWrites(t *testing.T) {
	home := t.TempDir()

	err := ensureCodexCredentials(home, codexHomeRealHost, resolveOpenAIAPIKey(nil, nil))
	assert.ErrorContains(t, err, "codex login")
	assert.ErrorContains(t, err, "ctxloom does not write this home")
	_, statErr := os.Stat(filepath.Join(home, ConfigDirName))
	assert.True(t, os.IsNotExist(statErr),
		"the failing verification must not have CREATED the real home it was checking")

	codexDir := filepath.Join(home, ConfigDirName)
	require.NoError(t, os.MkdirAll(codexDir, 0o700))
	authPath := filepath.Join(codexDir, AuthFileName)
	require.NoError(t, os.WriteFile(authPath, []byte(`{"tokens":"host"}`), 0o600))

	before, err := os.ReadFile(authPath)
	require.NoError(t, err)
	assert.NoError(t, ensureCodexCredentials(home, codexHomeRealHost, resolveOpenAIAPIKey(nil, nil)))
	after, err := os.ReadFile(authPath)
	require.NoError(t, err)
	assert.Equal(t, before, after, "verification is a READ; the host's own credential bytes are untouched")
}

// TestEnsureCodexCredentials_ContainerFresh_VerifiesMount pins the core
// assertion: the container axis VERIFIES the bind-mounted auth.json
// rather than copying (copying would collide with the read-only mount).
func TestEnsureCodexCredentials_ContainerFresh_VerifiesMount(t *testing.T) {
	home := t.TempDir()
	// No auth.json yet — the mount "didn't land".
	err := ensureCodexCredentials(home, codexHomeContainerFresh, resolveOpenAIAPIKey(nil, nil))
	assert.ErrorContains(t, err, "codex auth mount did not land")

	// Now simulate the mount having landed.
	codexDir := filepath.Join(home, ".codex")
	assert.NoError(t, os.MkdirAll(codexDir, 0o700))
	assert.NoError(t, os.WriteFile(filepath.Join(codexDir, AuthFileName), []byte(`{}`), 0o600))
	assert.NoError(t, ensureCodexCredentials(home, codexHomeContainerFresh, resolveOpenAIAPIKey(nil, nil)))
}

// TestResolveCodexProjectDir_UnexpectedShape_IsAnnounced pins that the one
// resolution branch that does NOT deliver what it was handed says so. A user's
// `env: {CODEX_HOME: /custom/home}` rides llmEnvFor -> RunOptions.Env -> here
// (internal/cli/llm_resolve.go), and codex nests its own ".codex" under it, so
// the child receives /custom/home/.codex — never the requested path. The
// nesting is deliberate (every cell-scoped writer joins ConfigDirName itself);
// doing it without a word was not.
func TestResolveCodexProjectDir_UnexpectedShape_IsAnnounced(t *testing.T) {
	var dir string
	out := captureStderr(t, func() {
		dir, _ = resolveCodexProjectDir(map[string]string{CodexHomeEnv: "/custom/home"}, "/proj", agent.CellKindShared)
	})
	assert.Equal(t, "/custom/home", dir)
	assert.Contains(t, out, "/custom/home", "the warning names the requested CODEX_HOME")
	assert.Contains(t, out, filepath.Join("/custom/home", ConfigDirName), "and the one the child actually gets")
}

// The expected "/.codex"-suffixed shape (every isolation-provided value) is
// delivered verbatim and must stay silent — a warning on the normal path would
// fire on every isolated run.
func TestResolveCodexProjectDir_ExpectedShape_IsSilent(t *testing.T) {
	out := captureStderr(t, func() {
		dir, src := resolveCodexProjectDir(map[string]string{CodexHomeEnv: "/cfg/home/.codex"}, "/proj", agent.CellKindShared)
		assert.Equal(t, "/cfg/home", dir)
		assert.Equal(t, codexHomeIsolationProvided, src)
	})
	assert.Empty(t, out)
}

// TestCodex_Setup_PerAgentEnvKeyAuthenticates pins the credential gate against
// the env the CHILD will actually receive, not the ambient process env. A
// per-agent `env:` OPENAI_API_KEY reaches the child two ways — the run env
// (SetupRequest.Env, which is also where the gate's sibling resolution reads
// CODEX_HOME from) and the backend config env (CodexConfig.Env, applied by
// BaseBackend.BuildEnv) — so a gate that consults only os.Getenv refuses to
// launch a run that would have authenticated perfectly well.
func TestCodex_Setup_PerAgentEnvKeyAuthenticates(t *testing.T) {
	// An empty HOME (so the real-home verification would find no auth.json and
	// fail) isolates the env-key question from everything else.
	setupNoCredentials := func(t *testing.T) {
		t.Helper()
		t.Setenv("HOME", t.TempDir())
	}

	t.Run("run env", func(t *testing.T) {
		t.Setenv("OPENAI_API_KEY", "")
		setupNoCredentials(t)

		b := NewCodex()
		_ = b.Setup(context.Background(), &agent.SetupRequest{
			WorkDir: t.TempDir(),
			Env:     map[string]string{"OPENAI_API_KEY": "sk-per-agent"},
		})
		assert.NoError(t, b.credentialErr, "the run env carries the key onto the child, so codex authenticates")
	})

	t.Run("backend config env", func(t *testing.T) {
		t.Setenv("OPENAI_API_KEY", "")
		setupNoCredentials(t)

		b := NewCodex()
		b.Configure(&CodexConfig{Env: map[string]string{"OPENAI_API_KEY": "sk-per-agent"}})
		_ = b.Setup(context.Background(), &agent.SetupRequest{WorkDir: t.TempDir()})
		assert.NoError(t, b.credentialErr, "BuildEnv puts the config env on the child, so codex authenticates")
	})

	t.Run("run env wins an empty override", func(t *testing.T) {
		t.Setenv("OPENAI_API_KEY", "sk-ambient")
		setupNoCredentials(t)

		b := NewCodex()
		_ = b.Setup(context.Background(), &agent.SetupRequest{
			WorkDir: t.TempDir(),
			Env:     map[string]string{"OPENAI_API_KEY": ""},
		})
		assert.Error(t, b.credentialErr,
			"an explicit empty run-env value overrides the ambient key on the child, so the credential check must still bite")
	})
}

// TestCodex_SetupExecute_AgreeOnIsolatedCodexHome is the end-to-end PAYLOAD
// test for the precedence bug: Setup (delivery) and cellCodexHomeEnv
// (Execute's env) must resolve to the IDENTICAL CODEX_HOME when isolation
// provides one — this is the assertion that would have caught the original
// bug (the isolation-provided value being silently overridden by the
// backend's own <WorkDir>/.codex).
func TestCodex_SetupExecute_AgreeOnIsolatedCodexHome(t *testing.T) {
	b := NewCodex()
	isolatedHome := filepath.Join(t.TempDir(), ".codex")
	setupReq := &agent.SetupRequest{
		WorkDir: "/proj",
		Env:     map[string]string{"CODEX_HOME": isolatedHome},
	}
	// Setup best-effort delivers files (may warn on I/O in a bare temp tree);
	// what matters here is the resolved state it stashes, not delivery success.
	_ = b.Setup(context.Background(), setupReq)
	assert.Equal(t, filepath.Dir(isolatedHome), b.resolvedProjectDir)
	assert.NotEmpty(t, b.resolvedTrustAbsPath, "an isolation-provided home is trusted-pre-seed eligible")

	execEnv := b.cellCodexHomeEnv(&agent.ExecuteRequest{WorkDir: "/proj", Env: setupReq.Env})
	assert.Equal(t, isolatedHome, execEnv["CODEX_HOME"], "Execute's CODEX_HOME matches exactly what Setup delivered into")
}

// TestCodex_SetupExecute_NoControlledHomeAgreesOnTheRealHostHome is D2's
// end-to-end shape: with NO isolation-provided CODEX_HOME (None/shared-cwd, no
// binding or a `host` one), Setup's delivery target and Execute's CODEX_HOME
// both land on the user's real ~/.codex — and trust is NOT pre-seeded, because
// that home is the user's own and carries codex's own accumulated
// `[projects."..."]` answers. ctxloom answers a trust prompt only for a home it
// created (docs/trust-model.md).
func TestCodex_SetupExecute_NoControlledHomeAgreesOnTheRealHostHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	require.NoError(t, os.MkdirAll(filepath.Join(home, ConfigDirName), 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(home, ConfigDirName, AuthFileName), []byte(`{}`), 0o600))
	workDir := t.TempDir()

	b := NewCodex()
	_ = b.Setup(context.Background(), &agent.SetupRequest{WorkDir: workDir})
	assert.Equal(t, home, b.resolvedProjectDir)
	assert.Empty(t, b.resolvedTrustAbsPath,
		"the real ~/.codex is the user's own; ctxloom must not write a trust answer into it")
	assert.NoError(t, b.credentialErr, "the host's own auth.json authenticates the run")

	execEnv := b.cellCodexHomeEnv(&agent.ExecuteRequest{WorkDir: workDir})
	assert.Equal(t, filepath.Join(home, ConfigDirName), execEnv[CodexHomeEnv])
}

// TestCodex_SetupExecute_SessionInstanceAgreesAndIsTrustSeeded is the same
// end-to-end shape for a `config_home: project` run, whose per-session instance
// arrives as an isolation-provided CODEX_HOME: Setup and Execute agree on it,
// and trust IS pre-seeded — the instance is a home ctxloom created and codex
// has never seen, so without the entry codex would re-prompt (or, under `codex
// exec`, proceed silently untrusted).
func TestCodex_SetupExecute_SessionInstanceAgreesAndIsTrustSeeded(t *testing.T) {
	workDir := t.TempDir()
	instance, err := SessionHome(workDir, "ugly-icy-squid")
	require.NoError(t, err)
	instanceHome := filepath.Join(instance, ConfigDirName)

	b := NewCodex()
	setupReq := &agent.SetupRequest{WorkDir: workDir, Env: map[string]string{CodexHomeEnv: instanceHome}}
	_ = b.Setup(context.Background(), setupReq)

	assert.Equal(t, instance, b.resolvedProjectDir)
	wantTrust, err := filepath.Abs(workDir)
	require.NoError(t, err)
	assert.Equal(t, wantTrust, b.resolvedTrustAbsPath,
		"the instance is a home codex has never seen, so trust is pre-seeded for the WORKING DIRECTORY")
	assert.NotEqual(t, b.resolvedProjectDir, b.resolvedTrustAbsPath,
		"codex keys trust by the cwd it runs in, NEVER by its home")
	assert.NoError(t, b.credentialErr, "the instance was prepared upstream; Setup does not re-verify it")

	execEnv := b.cellCodexHomeEnv(&agent.ExecuteRequest{WorkDir: workDir, Env: setupReq.Env})
	assert.Equal(t, instanceHome, execEnv[CodexHomeEnv])
}

// TestCodex_SetupExecute_ProcessIsolatedUsesContainerHome is the
// end-to-end Setup/Execute agreement test: a container cell's CODEX_HOME
// resolves to $HOME/.codex on BOTH the Setup (delivery) and Execute (env)
// sides, matching where codexCredentialMounts (isolation/auth.go) bind-mounts
// auth.json — never the bind-mounted PROJECT dir.
func TestCodex_SetupExecute_ProcessIsolatedUsesContainerHome(t *testing.T) {
	containerHome := t.TempDir()
	t.Setenv("HOME", containerHome)
	assert.NoError(t, os.MkdirAll(filepath.Join(containerHome, ".codex"), 0o700))
	assert.NoError(t, os.WriteFile(filepath.Join(containerHome, ".codex", AuthFileName), []byte(`{}`), 0o600))

	b := NewCodex()
	setupReq := &agent.SetupRequest{WorkDir: "/workspace/proj", CellKind: agent.CellKindProcessIsolated}
	err := b.Setup(context.Background(), setupReq)
	_ = err // best-effort delivery; the resolved state is what this test pins
	assert.Equal(t, containerHome, b.resolvedProjectDir)
	assert.NotEmpty(t, b.resolvedTrustAbsPath, "a container's fresh home is ephemeral — safe to pre-seed trust into")
	assert.NoError(t, b.credentialErr, "the bind-mounted auth.json verifies clean")

	execEnv := b.cellCodexHomeEnv(&agent.ExecuteRequest{WorkDir: "/workspace/proj", CellKind: agent.CellKindProcessIsolated})
	assert.Equal(t, filepath.Join(containerHome, ".codex"), execEnv["CODEX_HOME"], "Execute's CODEX_HOME matches exactly what Setup verified")
}

// TestCodex_Execute_RefusesToLaunchOnCredentialFailure is the FAIL-LOUD
// contract at the Execute boundary: a non-nil b.credentialErr (stashed by
// Setup) makes Execute return it immediately WITHOUT calling ExecuteCLI —
// codex must never spawn against an unauthenticated CODEX_HOME. Setup's own
// errors are fault-tolerantly warned-and-ignored upstream (grpc/server.go),
// so this Execute-side refusal is the only place this failure can surface as
// a hard, non-zero-exit error.
func TestCodex_Execute_RefusesToLaunchOnCredentialFailure(t *testing.T) {
	b := NewCodex()
	sentinel := fmt.Errorf("codex: no OPENAI_API_KEY and no host ~/.codex/auth.json credentials found")
	b.credentialErr = sentinel
	// An invalid binary path proves ExecuteCLI was never reached: if Execute
	// fell through to it, this would fail to exec and return a DIFFERENT
	// error (exec: "...": executable file not found), not the sentinel.
	b.BinaryPath = "/nonexistent/codex-binary-that-must-never-run"

	result, err := b.Execute(context.Background(), &agent.ExecuteRequest{Mode: agent.ModeOneshot, Prompt: &agent.Fragment{Content: "x"}}, io.Discard, io.Discard)
	assert.Nil(t, result)
	assert.Equal(t, sentinel, err, "Execute returns the stashed credential error verbatim, never a spawn attempt")
}

// TestCodex_CellCodexHomeEnv_SkipsSetup: SkipSetup (minimal/distill) sets no
// CODEX_HOME at all — codex keeps its global home, matching pre-fix behavior.
func TestCodex_CellCodexHomeEnv_SkipsSetup(t *testing.T) {
	b := NewCodex()
	assert.Nil(t, b.cellCodexHomeEnv(&agent.ExecuteRequest{WorkDir: "/proj", SkipSetup: true}))
}

// TestCodexFileExists pins this package's copy of the "existing regular file"
// predicate; see internal/cli's TestFileExists for why all three verbatim
// copies are pinned separately rather than compared to each other.
func TestCodexFileExists(t *testing.T) {
	dir := t.TempDir()
	regular := filepath.Join(dir, "config.toml")
	require.NoError(t, os.WriteFile(regular, []byte(""), 0o644))

	assert.True(t, codexFileExists(regular), "an existing regular file exists")
	assert.False(t, codexFileExists(dir), "a DIRECTORY is not a file")
	assert.False(t, codexFileExists(filepath.Join(dir, "absent.toml")), "a missing path does not exist")
}

// singleArgLimitForTest mirrors, independently of the production code, the
// largest byte length one argv element may carry on this host: Linux caps a
// SINGLE argument at MAX_ARG_STRLEN = 32 * PAGE_SIZE bytes INCLUDING the
// terminating NUL, regardless of the far larger total ARG_MAX. Probed on this
// box (4096-byte pages): a 131071-byte argument execs, 131072 fails E2BIG.
func singleArgLimitForTest() int { return 32*os.Getpagesize() - 1 }

// TestCodex_Execute_RefusesOversizedPromptBeforeExec pins the fail-loud half of
// the argv capacity limit. codex carries the prompt as an argv positional
// (buildArgs), so a prompt past MAX_ARG_STRLEN cannot exec at all — and before
// this refusal the user saw only os/exec's generic
// "fork/exec /path/to/codex: argument list too long", which names neither the
// prompt nor its length and points at the TOTAL argument list, which is three
// orders of magnitude under ARG_MAX and entirely innocent.
//
// Truncating is not an option: a silently shortened prompt would run, answer a
// question nobody asked, and say nothing about it.
func TestCodex_Execute_RefusesOversizedPromptBeforeExec(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("MAX_ARG_STRLEN is a Linux per-argument cap; other platforms limit only the total")
	}
	oversize := singleArgLimitForTest() + 1
	prompt := strings.Repeat("x", oversize)

	launched := false
	b := NewCodex()
	b.SetLauncher(func(context.Context, agent.LaunchSpec, io.Reader, io.Writer, io.Writer, <-chan agent.WindowSize) (int32, error) {
		launched = true
		return 0, nil
	})

	_, err := b.Execute(context.Background(),
		&agent.ExecuteRequest{Mode: agent.ModeOneshot, Prompt: &agent.Fragment{Content: prompt}},
		io.Discard, io.Discard)

	require.Error(t, err, "a prompt past the single-argument limit cannot exec — it must be refused, not attempted")
	assert.False(t, launched, "the refusal must happen BEFORE exec, not as a launch failure")
	assert.Contains(t, err.Error(), strconv.Itoa(oversize),
		"the refusal must name the PROMPT'S OWN LENGTH as the cause; %q does not", err.Error())
	assert.Contains(t, err.Error(), "prompt",
		"the refusal must name the prompt as the oversized payload; %q does not", err.Error())
	assert.NotContains(t, err.Error(), "truncat",
		"nothing here may offer to shorten the prompt: %q", err.Error())
}

// TestCodex_Execute_PromptAtTheLimitReachesArgvUnchanged is the other half. A
// refusal that fires early breaks every ordinary run, and a prompt of EXACTLY
// the largest length exec accepts must still travel byte-for-byte.
func TestCodex_Execute_PromptAtTheLimitReachesArgvUnchanged(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("MAX_ARG_STRLEN is a Linux per-argument cap; other platforms limit only the total")
	}
	prompt := strings.Repeat("y", singleArgLimitForTest())

	var got []string
	b := NewCodex()
	b.SetLauncher(func(_ context.Context, spec agent.LaunchSpec, _ io.Reader, _, _ io.Writer, _ <-chan agent.WindowSize) (int32, error) {
		got = spec.Args
		return 0, nil
	})

	_, err := b.Execute(context.Background(),
		&agent.ExecuteRequest{Mode: agent.ModeOneshot, Prompt: &agent.Fragment{Content: prompt}},
		io.Discard, io.Discard)

	require.NoError(t, err, "a prompt at exactly the single-argument limit still execs and must not be refused")
	require.NotEmpty(t, got, "the engine must actually have been launched")
	last := got[len(got)-1]
	require.True(t, last == prompt,
		"the prompt must reach argv byte-for-byte: got %d bytes, want %d", len(last), len(prompt))
}
