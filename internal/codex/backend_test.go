package codex

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/stretchr/testify/assert"
)

// =============================================================================
// Capability wiring — codex is a full LaunchBackend agent.
// =============================================================================

func TestCodex_Capabilities(t *testing.T) {
	codex := NewCodex()
	assert.Equal(t, "codex", codex.Name())
	// tough-cloud S5: codex's SessionHistory scraper was deleted outright
	// (lived-zone: envelope-vs-flat parse mismatch); History() is nil now that
	// canonical capture is the only transcript source for codex.
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
// CODEX_HOME single ownership (white-dawn §2.2A) — resolveCodexProjectDir is
// the one function Setup's delivery target and Execute's env both read, so
// they can never disagree about where CODEX_HOME points. codex's credential
// seeding is now three-way (ensureCodexCredentials below): the isolation
// package's copy-based framework (credentialSeedSpecs["codex"], balmy-comic)
// for the worktree fan-out axis, plus codex's OWN active seed (in-tree host
// run, warm-yodel) and verify (container fresh-$HOME, dense-amaze) for the
// two axes the isolation package never sees.
// =============================================================================

// TestResolveCodexProjectDir_NoIsolation_FallsBackToWorkDir pins today's
// default (None/shared-cwd, or no backend context): no isolation-provided
// CODEX_HOME in env, not a container cell → the virtual project dir is
// WorkDir itself, in-tree, exactly as before this fix — codexHomeInTree (no
// trust pre-seed).
func TestResolveCodexProjectDir_NoIsolation_FallsBackToWorkDir(t *testing.T) {
	dir, source := resolveCodexProjectDir(nil, "/proj", agent.CellKindShared)
	assert.Equal(t, "/proj", dir)
	assert.Equal(t, codexHomeInTree, source)
}

// TestResolveCodexProjectDir_EmptyWorkDir mirrors cellCodexHomeEnv's old
// "" → "." fallback.
func TestResolveCodexProjectDir_EmptyWorkDir(t *testing.T) {
	dir, source := resolveCodexProjectDir(nil, "", agent.CellKindShared)
	assert.Equal(t, ".", dir)
	assert.Equal(t, codexHomeInTree, source)
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
	dir, source := resolveCodexProjectDir(map[string]string{"CODEX_HOME": "/custom/home"}, "/proj", agent.CellKindShared)
	assert.Equal(t, "/custom/home", dir)
	assert.Equal(t, codexHomeIsolationProvided, source)
}

// TestResolveCodexProjectDir_ProcessIsolated_UsesHomeEnv is dense-amaze's
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

// TestResolveCodexProjectDir_ProcessIsolated_NoHomeFallsBackToWorkDir covers
// the defensive fallback: a container cell with no $HOME set at all
// (unexpected — every container spec sets one) degrades to the same
// codexHomeInTree path a Shared cell takes, so it still gets ACTIVE seeding
// (ensureCodexCredentials) rather than silently trusting an unverified home.
func TestResolveCodexProjectDir_ProcessIsolated_NoHomeFallsBackToWorkDir(t *testing.T) {
	t.Setenv("HOME", "")
	dir, source := resolveCodexProjectDir(nil, "/workspace/proj", agent.CellKindProcessIsolated)
	assert.Equal(t, "/workspace/proj", dir)
	assert.Equal(t, codexHomeInTree, source)
}

// =============================================================================
// ensureCodexCredentials — the fail-loud gate warm-yodel/dense-amaze added:
// Setup resolves it and stashes b.credentialErr; Execute refuses to launch
// codex at all when it is set (see the Execute tests further below).
// =============================================================================

// TestEnsureCodexCredentials_IsolationProvided_NoOp pins that the worktree
// fan-out axis is a deliberate no-op here: it is already seeded (and already
// fails loud) upstream by isolation.Prepare, before this run starts.
func TestEnsureCodexCredentials_IsolationProvided_NoOp(t *testing.T) {
	restoreSeed := stubSeedCodexHomeFn(t, func(string) (bool, error) {
		t.Fatal("seedCodexHomeFn must not be called for codexHomeIsolationProvided")
		return false, nil
	})
	defer restoreSeed()
	assert.NoError(t, ensureCodexCredentials("/tmp/ctxloom-cfg-x", codexHomeIsolationProvided))
}

// TestEnsureCodexCredentials_EnvTrigger_SkipsEverything pins OPENAI_API_KEY
// as codex's envTrigger: present, it bypasses BOTH the in-tree seed call and
// the container mount verification — matching resolveCodexContainerAuth's
// and hostCredentialSeed's own envTrigger precedence.
func TestEnsureCodexCredentials_EnvTrigger_SkipsEverything(t *testing.T) {
	t.Setenv("OPENAI_API_KEY", "sk-test")
	restoreSeed := stubSeedCodexHomeFn(t, func(string) (bool, error) {
		t.Fatal("seedCodexHomeFn must not be called when OPENAI_API_KEY is set")
		return false, nil
	})
	defer restoreSeed()
	assert.NoError(t, ensureCodexCredentials(t.TempDir(), codexHomeInTree))
	assert.NoError(t, ensureCodexCredentials(t.TempDir(), codexHomeContainerFresh))
}

// TestEnsureCodexCredentials_InTree_SeedsViaIsolation is warm-yodel's core
// assertion: the in-tree axis actively calls the copy-based seed seam.
func TestEnsureCodexCredentials_InTree_SeedsViaIsolation(t *testing.T) {
	var calledWith string
	restoreSeed := stubSeedCodexHomeFn(t, func(dir string) (bool, error) {
		calledWith = dir
		return false, nil
	})
	defer restoreSeed()
	assert.NoError(t, ensureCodexCredentials("/tmp/proj", codexHomeInTree))
	assert.Equal(t, "/tmp/proj", calledWith)
}

// TestEnsureCodexCredentials_InTree_SeedFailureIsLoud pins the fail-loud
// contract: a seed failure (no host source) returns a non-nil, wrapped error
// naming the underlying problem — never a silent success.
func TestEnsureCodexCredentials_InTree_SeedFailureIsLoud(t *testing.T) {
	restoreSeed := stubSeedCodexHomeFn(t, func(string) (bool, error) {
		return false, fmt.Errorf("no OPENAI_API_KEY and no host ~/.codex/auth.json credentials found")
	})
	defer restoreSeed()
	err := ensureCodexCredentials("/tmp/proj", codexHomeInTree)
	assert.ErrorContains(t, err, "no host ~/.codex/auth.json credentials found")
}

// TestEnsureCodexCredentials_ContainerFresh_VerifiesMount is dense-amaze's
// core assertion: the container axis VERIFIES the bind-mounted auth.json
// rather than copying (copying would collide with the read-only mount).
func TestEnsureCodexCredentials_ContainerFresh_VerifiesMount(t *testing.T) {
	restoreSeed := stubSeedCodexHomeFn(t, func(string) (bool, error) {
		t.Fatal("seedCodexHomeFn (a COPY) must never be called for codexHomeContainerFresh")
		return false, nil
	})
	defer restoreSeed()

	home := t.TempDir()
	// No auth.json yet — the mount "didn't land".
	err := ensureCodexCredentials(home, codexHomeContainerFresh)
	assert.ErrorContains(t, err, "codex auth mount did not land")

	// Now simulate the mount having landed.
	codexDir := filepath.Join(home, ".codex")
	assert.NoError(t, os.MkdirAll(codexDir, 0o700))
	assert.NoError(t, os.WriteFile(filepath.Join(codexDir, codexAuthFileName), []byte(`{}`), 0o600))
	assert.NoError(t, ensureCodexCredentials(home, codexHomeContainerFresh))
}

// stubSeedCodexHomeFn substitutes the package-level seedCodexHomeFn seam for
// the duration of a test and returns the restore func — never touches the
// real host's ~/.codex or the real filesystem paths tests drive Setup
// against (e.g. "/proj").
func stubSeedCodexHomeFn(t *testing.T, fn func(string) (bool, error)) func() {
	t.Helper()
	orig := seedCodexHomeFn
	seedCodexHomeFn = fn
	return func() { seedCodexHomeFn = orig }
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

// TestCodex_SetupExecute_NoneCellUnchanged pins the deliberately-scoped
// residual: with NO isolation-provided CODEX_HOME (None/shared-cwd), Setup
// and Execute both still land on <WorkDir>/.codex — today's behavior,
// unchanged by this fix's dir RESOLUTION. Credential SEEDING is new
// (warm-yodel): the in-tree axis now actively calls seedCodexHomeFn, stubbed
// here to keep the test hermetic (never touches the real host's ~/.codex or
// writes into the fake "/proj").
func TestCodex_SetupExecute_NoneCellUnchanged(t *testing.T) {
	var seededDir string
	restoreSeed := stubSeedCodexHomeFn(t, func(dir string) (bool, error) {
		seededDir = dir
		return false, nil
	})
	defer restoreSeed()

	b := NewCodex()
	setupReq := &agent.SetupRequest{WorkDir: "/proj"}
	_ = b.Setup(context.Background(), setupReq)
	assert.Equal(t, "/proj", b.resolvedProjectDir)
	assert.Empty(t, b.resolvedTrustAbsPath, "in-tree config.toml is never trust-pre-seeded")
	assert.Equal(t, "/proj", seededDir, "Setup actively seeds the in-tree CODEX_HOME (warm-yodel)")
	assert.NoError(t, b.credentialErr)

	execEnv := b.cellCodexHomeEnv(&agent.ExecuteRequest{WorkDir: "/proj"})
	assert.Equal(t, filepath.Join("/proj", ".codex"), execEnv["CODEX_HOME"])
}

// TestCodex_SetupExecute_ProcessIsolatedUsesContainerHome is dense-amaze's
// end-to-end Setup/Execute agreement test: a container cell's CODEX_HOME
// resolves to $HOME/.codex on BOTH the Setup (delivery) and Execute (env)
// sides, matching where codexCredentialMounts (isolation/auth.go) bind-mounts
// auth.json — never the bind-mounted PROJECT dir.
func TestCodex_SetupExecute_ProcessIsolatedUsesContainerHome(t *testing.T) {
	containerHome := t.TempDir()
	t.Setenv("HOME", containerHome)
	assert.NoError(t, os.MkdirAll(filepath.Join(containerHome, ".codex"), 0o700))
	assert.NoError(t, os.WriteFile(filepath.Join(containerHome, ".codex", codexAuthFileName), []byte(`{}`), 0o600))

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
