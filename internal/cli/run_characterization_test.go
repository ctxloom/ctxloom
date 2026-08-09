package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// =============================================================================
// runRun characterization suite
// =============================================================================
//
// These tests exist to make `runRun`'s DECOMPOSITION safe, not to specify new
// behaviour. They were written and run GREEN against the 957-line, CCN-92
// single-body version of runRun before a single statement moved, so the
// extraction that followed had a net under it: a seam that changed behaviour
// while it was being moved fails here instead of looking identical to one that
// did not.
//
// The reachable surface is `--dry-run`, which is exactly why it is worth so
// much here: a dry run walks flag validation, config load + upgrade prompts,
// prompt finalization, launch-source resolution (--agent / default agent /
// -p/-f/-t), the strictness gate, and request-input preparation, then returns
// BEFORE anything stateful or interactive (no session index write, no task
// seeding, no coordinator, no isolation, no plugin launch). That is the whole
// front half of the function driven through its real CLI entry point.
//
// What these deliberately do NOT reach: everything past the dry-run return —
// session assignment, the coordinator standup, permission-posture warnings,
// isolation.Prepare, transport selection, terminal setup and the vpio launch.
// Those need a live engine and are covered by tests/acceptance/features/
// run.feature's `run --print` scenarios instead.

// runCLIFixture writes the smallest project config the run path needs (the
// same shape as the acceptance suite's minimalConfig) into a fresh isolated
// project dir, plus a bundle/fragment/profile so an assembly actually
// resolves. `config create`'s generated config is deliberately NOT used: its
// default profile carries uninstalled remote parents, which is itself a
// characterized behaviour below (the startup gate aborts on it).
func runCLIFixture(t *testing.T) string {
	t.Helper()
	dir := testsupport.ProjectDir(t)
	config.Invalidate()
	t.Cleanup(config.Invalidate)

	require.NoError(t, os.MkdirAll(filepath.Join(dir, ".ctxloom"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, ".ctxloom", "config.yaml"),
		[]byte("version: 4\n"), 0o644))
	// editor.command lives in HOME, not the project fixture: it is
	// ScopeMachine (internal/config/layerscope) — a binary on THIS box — so
	// a committed PROJECT file may no longer carry it (a startup-gate fatal
	// finding, aborting `ctxloom run` outright, unlike --config-set/env
	// misuse elsewhere in this suite which merely warn).
	home, err := os.UserHomeDir()
	require.NoError(t, err)
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".ctxloom"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(home, ".ctxloom", "config.yaml"),
		[]byte("version: 4\neditor:\n  command: \"true\"\n"), 0o644))
	config.Invalidate()

	for _, argv := range [][]string{
		{"bundle", "create", "demo", "-d", "characterization fixture"},
		{"fragment", "create", "demo", "testing"},
		{"profile", "create", "dev", "-b", "demo", "-d", "characterization fixture"},
	} {
		res := runCLI(t, argv...)
		require.NoError(t, res.err, "fixture %v: %s", argv, res.stderr)
	}
	return dir
}

// cliResult is one Execute() of the root command with all three output
// channels captured: cobra's own writer (where emit() renders --format json),
// the process stdout the dry-run text renderer writes to with fmt.Println, and
// the process stderr clidiag warnings go to.
type cliResult struct {
	out    string // cobra's out/err writer
	stdout string // os.Stdout
	stderr string // os.Stderr
	err    error
}

func (r cliResult) all() string { return r.out + r.stdout + r.stderr }

// runCLI drives rootCmd.Execute() with args, capturing every output channel
// and restoring the run command's package-level flag variables afterwards.
// The flag vars are process-global and pflag does not reset them between
// Execute() calls on a shared command tree, so a leaked --profile from one
// case would silently steer the next.
func runCLI(t *testing.T, args ...string) cliResult {
	t.Helper()

	savedFlags := struct {
		llm, agent, workspace, permissions, prompt, profile, savedPrompt string
		session, seedTask, seedStatus                                    string
		fragments, tags                                                  []string
		dryRun, print, structured, plain, assumeYes, distill             bool
		verbosity                                                        int
	}{
		runLLM, runAgent, runWorkspace, runPermissions, runPrompt, runProfile, runSavedPrompt,
		runResumeSession, runSeedTask, runSeedStatus,
		runFragments, runTags,
		runDryRun, runPrint, runStructured, runPlainTerminal, runAssumeYes, runResumeDistill,
		runVerbosity,
	}
	defer func() {
		runLLM, runAgent, runWorkspace = savedFlags.llm, savedFlags.agent, savedFlags.workspace
		runPermissions, runPrompt, runProfile = savedFlags.permissions, savedFlags.prompt, savedFlags.profile
		runSavedPrompt, runResumeSession = savedFlags.savedPrompt, savedFlags.session
		runSeedTask, runSeedStatus = savedFlags.seedTask, savedFlags.seedStatus
		runFragments, runTags = savedFlags.fragments, savedFlags.tags
		runDryRun, runPrint, runStructured = savedFlags.dryRun, savedFlags.print, savedFlags.structured
		runPlainTerminal, runAssumeYes, runResumeDistill = savedFlags.plain, savedFlags.assumeYes, savedFlags.distill
		runVerbosity = savedFlags.verbosity
	}()

	var cobraOut bytes.Buffer
	rootCmd.SetOut(&cobraOut)
	rootCmd.SetErr(&cobraOut)
	rootCmd.SetArgs(args)

	realOut, realErr := os.Stdout, os.Stderr
	outR, outW, _ := os.Pipe()
	errR, errW, _ := os.Pipe()
	os.Stdout, os.Stderr = outW, errW

	execErr := rootCommand().Execute()

	_ = outW.Close()
	_ = errW.Close()
	os.Stdout, os.Stderr = realOut, realErr

	var outBuf, errBuf bytes.Buffer
	_, _ = outBuf.ReadFrom(outR)
	_, _ = errBuf.ReadFrom(errR)

	rootCmd.SetOut(nil)
	rootCmd.SetErr(nil)
	rootCmd.SetArgs(nil)
	resetFlagState(rootCmd)
	config.Invalidate()

	return cliResult{out: cobraOut.String(), stdout: outBuf.String(), stderr: errBuf.String(), err: execErr}
}

// resetFlagState returns every flag in the command tree to its declared
// default AND clears pflag's per-flag `Changed` bit. Both halves matter across
// Execute() calls on a process-global command tree: the value carries a stale
// --format into the next case, and the Changed bit is what cobra's
// MarkFlagsMutuallyExclusive reads — so without this, one case passing
// --profile makes a later case passing --agent look like both were set at once.
func resetFlagState(c *cobra.Command) {
	reset := func(f *pflag.Flag) {
		if !f.Changed {
			return
		}
		if sv, ok := f.Value.(pflag.SliceValue); ok {
			_ = sv.Replace(nil)
		} else {
			_ = f.Value.Set(f.DefValue)
		}
		f.Changed = false
	}
	c.Flags().VisitAll(reset)
	c.PersistentFlags().VisitAll(reset)
	for _, sub := range c.Commands() {
		resetFlagState(sub)
	}
}

// -----------------------------------------------------------------------------
// Flag validation — the friction-up-front window, before any work happens
// -----------------------------------------------------------------------------

// A --permissions value that is not a known posture is rejected before config
// is even loaded, so a typo cannot silently resolve to a more permissive
// default. --distill without --session is rejected in the same window.
func TestRunCharacterization_FlagValidationRejectsBeforeAnyWork(t *testing.T) {
	runCLIFixture(t)

	res := runCLI(t, "run", "--dry-run", "--format", "text", "--permissions", "no-such-posture", "-p", "dev", "hi")
	require.Error(t, res.err, "an unknown --permissions value must be a hard error")
	assert.NotContains(t, res.all(), "=== LLM ===", "validation must reject before assembly renders anything")

	res = runCLI(t, "run", "--dry-run", "--format", "text", "--distill", "-p", "dev", "hi")
	require.Error(t, res.err, "--distill without --session must be rejected")
	assert.Contains(t, res.err.Error(), "--distill requires --session")
}

// -----------------------------------------------------------------------------
// The strictness gate that stands between assembly and launch
// -----------------------------------------------------------------------------

// `config create` writes a default profile whose parents are remote bundles that
// are not installed. config.Load downgrades that to warnings; the run path
// surfaces them AND records them as fatal findings, so the startup gate aborts
// with the dedicated exit code rather than launching an empty-context session.
// A dry run is gated too — previewing a broken setup must say so.
func TestRunCharacterization_StartupGateAbortsOnRecordedFindings(t *testing.T) {
	testsupport.ProjectDir(t)
	config.Invalidate()
	t.Cleanup(config.Invalidate)

	require.NoError(t, runCLI(t, "config", "create").err)

	res := runCLI(t, "run", "--dry-run", "--format", "text", "hello")
	require.Error(t, res.err, "unresolvable default-profile parents must abort the startup gate")

	var exitErr *ExitError
	require.ErrorAs(t, res.err, &exitErr, "a strict startup abort carries its own exit code")
	assert.Equal(t, exitCodeFatalFindings, exitErr.Code)
	assert.Contains(t, res.all(), "aborting startup")
	assert.Contains(t, res.all(), "--degraded", "the abort names the escape hatch")
	assert.NotContains(t, res.all(), "=== LLM ===", "the gate fires before the dry-run preview is rendered")
}

// -----------------------------------------------------------------------------
// Launch-source resolution: the -p / -f / -t classic-assembly arm
// -----------------------------------------------------------------------------

// An explicit -f selection whose fragments all fail to resolve must not exit 0
// having delivered no context. The check is on MissingFragments, not on a
// count of what loaded, because the always-on builtin companion fragments mean
// FragmentsLoaded is never empty.
func TestRunCharacterization_ExplicitFragmentMissFailsLoudly(t *testing.T) {
	runCLIFixture(t)

	res := runCLI(t, "run", "--dry-run", "--format", "text", "-f", "no-such-fragment", "hello")
	require.Error(t, res.err)
	assert.Contains(t, res.err.Error(), "no fragments loaded: requested fragments not found: no-such-fragment")

	// The discriminator: a PARTIAL -f resolution does not take the
	// "no fragments loaded" arm — but it does not launch either. The fragment
	// that failed to load was recorded as a fatal finding during assembly, so
	// the startup gate aborts with its own exit code instead.
	res = runCLI(t, "run", "--dry-run", "--format", "text", "-f", "testing", "-f", "no-such-fragment", "hello")
	require.Error(t, res.err, "a partial fragment resolution still carries an unresolved reference")
	assert.NotContains(t, res.err.Error(), "no fragments loaded",
		"a partial resolution is NOT the empty-selection error")
	var exitErr *ExitError
	require.ErrorAs(t, res.err, &exitErr)
	assert.Equal(t, exitCodeFatalFindings, exitErr.Code, "it aborts at the startup gate, not the assembly guard")
}

// The tag counterpart of the guard above.
func TestRunCharacterization_ExplicitTagMissFailsLoudly(t *testing.T) {
	runCLIFixture(t)

	res := runCLI(t, "run", "--dry-run", "--format", "text", "-t", "no-such-tag-anywhere", "hello")
	require.Error(t, res.err)
	assert.Contains(t, res.err.Error(), "no fragments loaded: no fragment matches tag(s): no-such-tag-anywhere")
}

// An unknown --agent is a HARD error: an explicit name is user intent.
func TestRunCharacterization_UnknownAgentIsAHardError(t *testing.T) {
	runCLIFixture(t)

	res := runCLI(t, "run", "--dry-run", "--format", "text", "--agent", "no-such-agent", "hello")
	require.Error(t, res.err, "an explicitly named --agent that does not resolve must not degrade")
	assert.NotContains(t, res.all(), "=== LLM ===")
}

// -----------------------------------------------------------------------------
// The dry-run payload: the resolved assembly a flag set produces
// -----------------------------------------------------------------------------

// The --format json shape the VSCode profile composer reads. Agent/Workspace/
// Runtime are omitempty; LLM/Backend/Profiles/Fragments/Context/Tokens are not.
// Profiles and Fragments render as [] rather than null (orEmpty).
func TestRunCharacterization_DryRunJSONPayload(t *testing.T) {
	runCLIFixture(t)

	res := runCLI(t, "run", "--dry-run", "--format", "json", "-p", "dev", "hello", "world")
	require.NoError(t, res.err, res.all())

	var got dryRunJSON
	require.NoError(t, json.Unmarshal([]byte(res.out), &got), "payload: %s", res.out)

	assert.Equal(t, "hello world", got.Prompt, "positional args join with a single space")
	assert.Equal(t, []string{"dev"}, got.Profiles)
	assert.NotEmpty(t, got.LLM)
	assert.Equal(t, got.LLM, got.Backend, "the fixture's label resolves to a same-named backend")
	assert.Contains(t, got.Context, "# testing", "the profile's fragment reaches the assembled context")
	assert.Positive(t, got.Tokens, "the token estimate is computed over the assembled context")
	assert.Contains(t, got.Fragments, "ctxloom:local@bundles/demo#fragments/testing")

	// Not an --agent run: the three axis fields stay omitted entirely.
	assert.NotContains(t, res.out, `"agent"`)
	assert.NotContains(t, res.out, `"workspace"`)
	assert.NotContains(t, res.out, `"runtime"`)
}

// --workspace is a SESSION trait resolved before the dry-run payload is built,
// so the preview reports the axis this run would actually use.
func TestRunCharacterization_DryRunJSONCarriesTheWorkspaceAxis(t *testing.T) {
	runCLIFixture(t)

	res := runCLI(t, "run", "--dry-run", "--format", "json", "--workspace", "worktree", "-p", "dev", "hi")
	require.NoError(t, res.err, res.all())

	var got dryRunJSON
	require.NoError(t, json.Unmarshal([]byte(res.out), &got))
	assert.Equal(t, "worktree", got.Workspace)
}

// The text renderer's section order and its three empty-state strings. This is
// user-visible output that a refactor must not reword or reorder.
func TestRunCharacterization_DryRunTextSections(t *testing.T) {
	runCLIFixture(t)

	res := runCLI(t, "run", "--dry-run", "--format", "text", "-p", "dev", "hello")
	require.NoError(t, res.err, res.all())
	body := res.all()

	for _, section := range []string{"=== LLM ===", "=== Profiles ===", "=== Fragments Loaded ===", "=== Prompt ===", "=== Context File ==="} {
		assert.Contains(t, body, section)
	}
	assert.Contains(t, body, "=== Assembled Context (~")
	assert.Contains(t, body, "Would write to: ")
	assert.Contains(t, body, "[hash].md")

	// Order is part of the contract.
	assert.Less(t, strings.Index(body, "=== LLM ==="), strings.Index(body, "=== Profiles ==="))
	assert.Less(t, strings.Index(body, "=== Profiles ==="), strings.Index(body, "=== Fragments Loaded ==="))
	assert.Less(t, strings.Index(body, "=== Fragments Loaded ==="), strings.Index(body, "=== Prompt ==="))
	assert.Less(t, strings.Index(body, "=== Prompt ==="), strings.Index(body, "=== Context File ==="))

	// No --agent binding: the "=== Agent ===" block is skipped entirely.
	assert.NotContains(t, body, "=== Agent ===")
}

// An empty prompt on an INTERACTIVE run legitimately means "open a session",
// and the text preview says so rather than showing a blank prompt section.
func TestRunCharacterization_DryRunTextEmptyStates(t *testing.T) {
	runCLIFixture(t)

	res := runCLI(t, "run", "--dry-run", "--format", "text", "-f", "testing")
	require.NoError(t, res.err, res.all())
	assert.Contains(t, res.all(), "(interactive mode)", "an empty prompt is not an error for an interactive run")
	assert.Contains(t, res.all(), "(no profiles)", "a bare -f selection resolves no profiles")
}

// -----------------------------------------------------------------------------
// Prompt finalization: source precedence
// -----------------------------------------------------------------------------

// --prompt wins over positional args; neither is consulted once an earlier
// source has produced a non-empty prompt.
func TestRunCharacterization_PromptSourcePrecedence(t *testing.T) {
	runCLIFixture(t)

	res := runCLI(t, "run", "--dry-run", "--format", "json", "-p", "dev", "--prompt", "from flag", "from", "args")
	require.NoError(t, res.err, res.all())

	var got dryRunJSON
	require.NoError(t, json.Unmarshal([]byte(res.out), &got))
	assert.Equal(t, "from flag", got.Prompt, "--prompt beats positional args")
}

// A --command naming a saved command that does not exist is a hard error, not
// a silent fall-through to the positional args.
func TestRunCharacterization_UnknownSavedCommandFails(t *testing.T) {
	runCLIFixture(t)

	res := runCLI(t, "run", "--dry-run", "--format", "text", "-p", "dev", "-r", "no-such-command", "fallback")
	require.Error(t, res.err)
	assert.Contains(t, res.err.Error(), "failed to load command")
}

// -----------------------------------------------------------------------------
// Deterministic resume, fault-tolerant arm
// -----------------------------------------------------------------------------

// A stale --session must never block the launch: full resume warns and
// proceeds with the assembled context unchanged (CLAUDE.md fault tolerance).
// Observable through the dry-run preview, which is built from the same
// ctxResult.Context the resume folds into.
//
// The harp used here is one AssignSession actually minted, so it exists in the
// session index but has no bound transcript yet — the "nothing to prime" arm.
// TestRunCharacterization_UnknownResumeSessionDegrades below covers the other
// arm, a harp that is not in the index at all.
func TestRunCharacterization_UnboundResumeSessionDegrades(t *testing.T) {
	dir := runCLIFixture(t)

	entry, err := operations.AssignSession(dir, "claude-code")
	require.NoError(t, err)

	base := runCLI(t, "run", "--dry-run", "--format", "json", "-p", "dev", "hi")
	require.NoError(t, base.err, base.all())
	var without dryRunJSON
	require.NoError(t, json.Unmarshal([]byte(base.out), &without))

	res := runCLI(t, "run", "--dry-run", "--format", "json", "--session", entry.HarpName, "-p", "dev", "hi")
	require.NoError(t, res.err, "a session with no bound transcript must not block the run")
	var with dryRunJSON
	require.NoError(t, json.Unmarshal([]byte(res.out), &with))

	assert.Equal(t, without.Context, with.Context, "an unprimeable harp leaves the assembled context untouched")
	assert.Contains(t, res.stderr, "no recorded history to prime", "the degrade is announced, not silent")
}

// The UNRESOLVABLE arm of the same contract: a harp that is not in the session
// index at all. This used to panic with a nil pointer dereference —
// operations.GetSession returns (nil, nil) for an absent harp and
// RecordedSessionEntries dereferenced entry.SessionID without checking — so a
// mistyped harp name, on a command whose argument is three random words, took
// the process down with a stack trace instead of reaching the warn
// resumeFullContext documents for exactly this case.
//
// Asserted at the CLI surface a user actually touches, because that is where
// the stack trace appeared. The primitive itself is pinned in
// operations.TestRecordedSessionEntries_UnknownHarpErrorsRatherThanPanicking,
// which also covers the ACP resume path and the coordinator's ended-child
// resume, both of which share the call.
func TestRunCharacterization_UnknownResumeSessionDegrades(t *testing.T) {
	runCLIFixture(t)

	base := runCLI(t, "run", "--dry-run", "--format", "json", "-p", "dev", "hi")
	require.NoError(t, base.err, base.all())
	var without dryRunJSON
	require.NoError(t, json.Unmarshal([]byte(base.out), &without))

	res := runCLI(t, "run", "--dry-run", "--format", "json", "--session", "no-such-harp-anywhere", "-p", "dev", "hi")
	require.NoError(t, res.err, "a harp that is not in the index must not block the run: %s", res.all())
	var with dryRunJSON
	require.NoError(t, json.Unmarshal([]byte(res.out), &with))

	assert.Equal(t, without.Context, with.Context, "an unresolvable harp leaves the assembled context untouched")
	assert.Contains(t, res.stderr, "no-such-harp-anywhere", "the degrade must name the harp it could not find")
	assert.Contains(t, res.stderr, "session list", "and say where the real harps are")
}

// -----------------------------------------------------------------------------
// Project-root resolution
// -----------------------------------------------------------------------------

// Outside a git repository the project root falls back to the launch
// directory. That keys tasks/plans/sessions to a path that will not follow the
// directory, so it warns — and never blocks.
func TestRunCharacterization_NonGitRootWarnsButProceeds(t *testing.T) {
	runCLIFixture(t)

	res := runCLI(t, "run", "--dry-run", "--format", "text", "-p", "dev", "hi")
	require.NoError(t, res.err, res.all())
	assert.Contains(t, res.stderr, "not in a git repository")
}
