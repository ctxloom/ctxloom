package cli

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/projectroot"
	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// TestBuildInjectContextOutput covers the wrapping logic that surrounds
// ctxloom-assembled context before the LLM sees it on SessionStart.
// Three cases: empty content (returns empty HookOutput to avoid
// misleading the LLM), non-empty content (gets header + tags + footer),
// and the SessionStart event name being set.
func TestBuildInjectContextOutput(t *testing.T) {
	t.Run("empty_content_yields_empty_output", func(t *testing.T) {
		out := buildInjectContextOutput("", "", 1, 1)
		assert.Nil(t, out.HookSpecificOutput,
			"empty content must NOT surface an AdditionalContext field — "+
				"otherwise the LLM sees the 'ctxloom content loaded' header "+
				"with nothing in it")
	})

	t.Run("wraps_content_with_header_and_footer", func(t *testing.T) {
		out := buildInjectContextOutput("rust rules go here", "", 1, 1)
		require.NotNil(t, out.HookSpecificOutput)
		body := out.HookSpecificOutput.AdditionalContext
		assert.Contains(t, body, "# Project Context (assembled by ctxloom)")
		assert.Contains(t, body, "<ctxloom-context>")
		assert.Contains(t, body, "rust rules go here")
		assert.Contains(t, body, "</ctxloom-context>")
		// Header must precede the content; footer must follow.
		hdrIdx := strings.Index(body, "<ctxloom-context>")
		ftrIdx := strings.Index(body, "</ctxloom-context>")
		contentIdx := strings.Index(body, "rust rules go here")
		assert.True(t, hdrIdx < contentIdx, "header must come before content")
		assert.True(t, contentIdx < ftrIdx, "content must come before footer")
	})

	t.Run("event_name_pinned", func(t *testing.T) {
		out := buildInjectContextOutput("anything", "", 1, 1)
		require.NotNil(t, out.HookSpecificOutput)
		assert.Equal(t, "SessionStart", out.HookSpecificOutput.HookEventName,
			"hook event name MUST be SessionStart — agent hook writers route by this string")
	})

	// Lock the hook's single-shot (unchunked, no-essence) framing to the shared
	// agent.FrameProjectContext that claude's --append-system-prompt-file
	// delivery uses, so the SessionStart-hook and native-flag paths can't drift.
	t.Run("single_shot_matches_frame_project_context", func(t *testing.T) {
		out := buildInjectContextOutput("shared framing body", "", 1, 1)
		require.NotNil(t, out.HookSpecificOutput)
		assert.Equal(t, agent.FrameProjectContext("shared framing body"),
			out.HookSpecificOutput.AdditionalContext,
			"the hook's single-shot output must equal the native-flag framing")
	})
}

// TestBuildInjectContextOutput_Segments covers the chunked form: a non-first
// segment carries a compact "segment k of N" header and NO attribution
// preamble, while still being independently framed in its own
// <ctxloom-context> block.
func TestBuildInjectContextOutput_Segments(t *testing.T) {
	first := buildInjectContextOutput("alpha body", "", 1, 3)
	require.NotNil(t, first.HookSpecificOutput)
	fb := first.HookSpecificOutput.AdditionalContext
	assert.Contains(t, fb, "segment 1 of 3")
	assert.Contains(t, fb, "Treat it as authoritative", "first segment carries the attribution preamble")
	assert.Contains(t, fb, "<ctxloom-context>")
	assert.Contains(t, fb, "</ctxloom-context>")

	later := buildInjectContextOutput("gamma body", "", 3, 3)
	require.NotNil(t, later.HookSpecificOutput)
	lb := later.HookSpecificOutput.AdditionalContext
	assert.Contains(t, lb, "segment 3 of 3")
	assert.NotContains(t, lb, "Treat it as authoritative", "later segments omit the preamble")
	assert.Contains(t, lb, "<ctxloom-context>", "every segment is self-framed")
	assert.Contains(t, lb, "</ctxloom-context>")
	assert.Contains(t, lb, "gamma body")
}

// TestShouldInjectResumedEssence covers the source gate: the resumed essence
// rides an initial launch but never /clear or /compact.
func TestShouldInjectResumedEssence(t *testing.T) {
	for _, src := range []string{"startup", "resume", "", "unexpected"} {
		assert.True(t, shouldInjectResumedEssence(src), "source %q should inject essence", src)
	}
	for _, src := range []string{"clear", "compact"} {
		assert.False(t, shouldInjectResumedEssence(src), "source %q must not inject essence", src)
	}
}

// TestResumePartsIncludeSession covers the parts gate: essence is for resumes
// that carried the session, not tasks-only resumes.
func TestResumePartsIncludeSession(t *testing.T) {
	assert.True(t, resumePartsIncludeSession(""), "empty parts default to session+tasks")
	assert.True(t, resumePartsIncludeSession("session,tasks"))
	assert.True(t, resumePartsIncludeSession("session"))
	assert.True(t, resumePartsIncludeSession("tasks, session "))
	assert.False(t, resumePartsIncludeSession("tasks"))
}

// TestBuildInjectContextOutput_ResumedEssence covers essence framing: it rides
// the first chunk (before the project context), stands alone when there is no
// project context, and never appears on later chunks.
func TestBuildInjectContextOutput_ResumedEssence(t *testing.T) {
	t.Run("essence_precedes_content_on_first_chunk", func(t *testing.T) {
		out := buildInjectContextOutput("project rules", "what we did last time", 1, 1)
		require.NotNil(t, out.HookSpecificOutput)
		body := out.HookSpecificOutput.AdditionalContext
		assert.Contains(t, body, "<ctxloom-resumed-session>")
		assert.Contains(t, body, "what we did last time")
		assert.Contains(t, body, "<ctxloom-context>")
		assert.Contains(t, body, "project rules")
		assert.Less(t, strings.Index(body, "ctxloom-resumed-session"),
			strings.Index(body, "ctxloom-context>"), "essence must precede project context")
	})

	t.Run("essence_alone_when_no_content", func(t *testing.T) {
		out := buildInjectContextOutput("", "prior essence", 1, 1)
		require.NotNil(t, out.HookSpecificOutput)
		body := out.HookSpecificOutput.AdditionalContext
		assert.Contains(t, body, "prior essence")
		assert.NotContains(t, body, "<ctxloom-context>")
	})

	t.Run("essence_omitted_on_later_chunks", func(t *testing.T) {
		out := buildInjectContextOutput("chunk 2 body", "prior essence", 2, 3)
		require.NotNil(t, out.HookSpecificOutput)
		body := out.HookSpecificOutput.AdditionalContext
		assert.NotContains(t, body, "prior essence", "essence rides only the first chunk")
		assert.NotContains(t, body, "<ctxloom-resumed-session>")
	})
}

// TestResumedEssenceForInjection covers the end-to-end gate that reads the
// resumed harp's essence.md, including the source, chunk, parts, and
// presence conditions.
func TestResumedEssenceForInjection(t *testing.T) {
	home := testsupport.Isolate(t)
	harp := "swift-amber-falcon"
	dir := filepath.Join(home, ".ctxloom", "sessions", harp)
	require.NoError(t, os.MkdirAll(dir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "essence.md"), []byte("  distilled summary  \n"), 0o644))

	assert.Equal(t, "distilled summary",
		resumedEssenceForInjection(1, "startup", harp, "session,tasks"), "trimmed essence on startup")
	assert.Empty(t, resumedEssenceForInjection(1, "clear", harp, "session,tasks"), "no essence on /clear")
	assert.Empty(t, resumedEssenceForInjection(2, "startup", harp, "session,tasks"), "no essence on later chunk")
	assert.Empty(t, resumedEssenceForInjection(1, "startup", "", "session,tasks"), "no essence without a resume")
	assert.Empty(t, resumedEssenceForInjection(1, "startup", harp, "tasks"), "no essence for tasks-only resume")
	assert.Empty(t, resumedEssenceForInjection(1, "startup", "no-such-harp", "session,tasks"), "no essence when file missing")
}

// TestSelectChunk covers chunk selection: single-shot (total<1) returns the
// whole content as 1/1, an in-range part returns that chunk, and an
// out-of-range part returns empty content (so the hook emits nothing).
func TestSelectChunk(t *testing.T) {
	t.Run("single_shot_returns_whole", func(t *testing.T) {
		c, p, total := selectChunk("everything", 0, 0)
		assert.Equal(t, "everything", c)
		assert.Equal(t, 1, p)
		assert.Equal(t, 1, total)
	})

	t.Run("in_range_and_out_of_range", func(t *testing.T) {
		// Build content large enough to split into multiple chunks.
		var b strings.Builder
		for i := range 6 {
			if i > 0 {
				b.WriteString("\n\n---\n\n")
			}
			b.WriteString("# S")
			b.WriteString(strings.Repeat("x", 3000))
		}
		content := b.String()

		c1, _, total := selectChunk(content, 1, 3)
		assert.NotEmpty(t, c1, "part 1 must return content")
		assert.Equal(t, 3, total)

		// A part beyond the available chunks yields empty (hook emits {}).
		cOut, _, _ := selectChunk(content, 999, 3)
		assert.Empty(t, cOut, "out-of-range part must return empty content")
	})
}

// TestResolveInjectContextWorkDir covers the three-branch precedence
// chain in inject-context's workDir resolver.
func TestResolveInjectContextWorkDir(t *testing.T) {
	// Neutralize any ambient CTXLOOM_ROOT so the git-root branches are
	// exercised deterministically; individual subtests set it as needed.
	t.Setenv(projectroot.EnvVar, "")

	t.Run("flag_wins", func(t *testing.T) {
		got := resolveInjectContextWorkDir("/explicit/project",
			func(string) (string, error) { return "/git/root", nil })
		assert.Equal(t, "/explicit/project", got,
			"--project flag must beat git-root discovery")
	})

	t.Run("env_wins_over_git", func(t *testing.T) {
		root := t.TempDir()
		t.Setenv(projectroot.EnvVar, root)
		got := resolveInjectContextWorkDir("",
			func(string) (string, error) { return "/git/root", nil })
		assert.Equal(t, root, got,
			"a valid CTXLOOM_ROOT must beat git-root discovery")
	})

	t.Run("flag_wins_over_env", func(t *testing.T) {
		t.Setenv(projectroot.EnvVar, t.TempDir())
		got := resolveInjectContextWorkDir("/explicit/project",
			func(string) (string, error) { return "/git/root", nil })
		assert.Equal(t, "/explicit/project", got,
			"--project flag is a deliberate per-call choice and must beat the env var")
	})

	t.Run("invalid_env_falls_through_to_git", func(t *testing.T) {
		t.Setenv(projectroot.EnvVar, filepath.Join(t.TempDir(), "does-not-exist"))
		got := resolveInjectContextWorkDir("",
			func(string) (string, error) { return "/git/root", nil })
		assert.Equal(t, "/git/root", got,
			"an invalid CTXLOOM_ROOT must fall through to git-root discovery")
	})

	t.Run("git_root_when_no_flag", func(t *testing.T) {
		got := resolveInjectContextWorkDir("",
			func(string) (string, error) { return "/git/root", nil })
		assert.Equal(t, "/git/root", got)
	})

	t.Run("dot_fallback_on_git_error", func(t *testing.T) {
		got := resolveInjectContextWorkDir("",
			func(string) (string, error) { return "", errors.New("not a repo") })
		assert.Equal(t, ".", got,
			"hook must keep working outside a git repo — silently fall back to cwd")
	})

	t.Run("dot_fallback_on_nil_finder", func(t *testing.T) {
		// Defensive: if a future refactor accidentally passes nil, don't
		// crash; treat the same as "no git root".
		got := resolveInjectContextWorkDir("", nil)
		assert.Equal(t, ".", got)
	})

	t.Run("flag_wins_even_when_finder_errors", func(t *testing.T) {
		// The flag is unconditional — finder errors shouldn't be visible.
		got := resolveInjectContextWorkDir("/p",
			func(string) (string, error) { return "", errors.New("ignored") })
		assert.Equal(t, "/p", got)
	})
}

// TestClearRecoveryMessage covers the user-facing /recover nudge gate: it fires
// only on a /clear, only on the first chunk, and only when the current session's
// pre-clear transcript is recoverable.
func TestClearRecoveryMessage(t *testing.T) {
	msg := clearRecoveryMessage("clear", 1, true)
	assert.Contains(t, msg, "/recover", "the nudge must name the /recover command")

	assert.Empty(t, clearRecoveryMessage("clear", 1, false),
		"no nudge when there's nothing to recover")
	assert.Empty(t, clearRecoveryMessage("clear", 2, true),
		"chunked injection shows the nudge once, on the first chunk")
	for _, src := range []string{"startup", "resume", "compact", ""} {
		assert.Empty(t, clearRecoveryMessage(src, 1, true),
			"source %q retains or re-injects context — nothing to recover", src)
	}
}

// TestCurrentSessionRecoverable covers the rotation-lineage gate behind the
// /clear recovery nudge: claude-code's /clear starts a fresh, necessarily
// EMPTY transcript file, so "is the current transcript non-empty" (the old
// check) can never fire right after a clear — the fix checks the harp's
// index entry instead (see the production doc comment for the measured
// premise this replaces).
//
// Two entry shapes both mean "recoverable", and the reason there are two is
// hook ORDERING: claude.go's ctxloomMachineCallbacks runs inject-context
// BEFORE session-bind. On the FIRST /clear in a session, session-bind (which
// appends the displaced binding to Rotations) has not run yet — the index
// still holds only the pre-clear SessionID, no rotation recorded. On a
// SECOND /clear in the same session, a rotation from the FIRST one already
// exists. Both must read as recoverable, or the very first clear a user ever
// makes would get no nudge at all.
func TestCurrentSessionRecoverable(t *testing.T) {
	testsupport.Isolate(t)

	mgr, err := sessions.Open("")
	require.NoError(t, err)

	// Shape 1: a PRIOR clear already recorded a rotation (a second-or-later
	// clear in this session). Recoverable regardless of what the incoming
	// payload's session id is.
	rotated, err := mgr.AssignHarp("/proj", "claude-code")
	require.NoError(t, err)
	require.NoError(t, mgr.BindSession(rotated.HarpName, "pre-clear-id", "/pre-clear.jsonl"))
	require.NoError(t, mgr.BindSession(rotated.HarpName, "post-clear-id", "/post-clear.jsonl"))

	// Shape 2: bound once, no rotation recorded yet (the FIRST clear in this
	// session — session-bind for THIS clear hasn't run). The incoming payload
	// carries a NEW id that differs from the entry's current binding: that
	// disagreement IS the not-yet-recorded displacement.
	displaced, err := mgr.AssignHarp("/proj", "claude-code")
	require.NoError(t, err)
	require.NoError(t, mgr.BindSession(displaced.HarpName, "pre-clear-id", "/pre-clear.jsonl"))

	// Shape 3: bound to the SAME id the incoming payload carries — an
	// idempotent rebind (e.g. a duplicate hook fire), not a displacement.
	// Nothing was thrown away.
	sameID, err := mgr.AssignHarp("/proj", "claude-code")
	require.NoError(t, err)
	require.NoError(t, mgr.BindSession(sameID.HarpName, "only-id", "/t.jsonl"))

	assert.True(t, currentSessionRecoverable("clear", rotated.HarpName, "post-clear-id"),
		"clear-source + rotation history present -> recoverable, regardless of the incoming payload id")
	assert.True(t, currentSessionRecoverable("clear", displaced.HarpName, "new-post-clear-id"),
		"clear-source + bound entry whose current id differs from the incoming payload id -> the displacement about to be recorded is itself the recoverable signal")
	assert.False(t, currentSessionRecoverable("clear", sameID.HarpName, "only-id"),
		"clear-source + entry already bound to the SAME id the payload carries -> nothing displaced, nothing to recover")

	for _, src := range []string{"startup", "resume", "compact", ""} {
		assert.False(t, currentSessionRecoverable(src, rotated.HarpName, "post-clear-id"),
			"source %q is not a /clear, even with rotation history present -> not recoverable", src)
		assert.False(t, currentSessionRecoverable(src, displaced.HarpName, "new-post-clear-id"),
			"source %q is not a /clear, even with a displaced binding present -> not recoverable", src)
	}

	assert.False(t, currentSessionRecoverable("clear", "", "any-id"),
		"an empty harp name is never recoverable")
	assert.False(t, currentSessionRecoverable("clear", "no-such-harp-in-the-index", "any-id"),
		"a harp the index has never heard of is never recoverable")
}

// TestInjectContextSystemMessageComposition pins the join behavior the
// inject-context RunE relies on for output.SystemMessage (the two
// SessionStart nudges — clear-recovery + agent-setup — coexisting):
// non-empty parts join with a blank line, empties drop. Previously covered
// via composeSystemMessage, a pure rename of operations.JoinLeadBlocks that
// was later deleted; the RunE now calls JoinLeadBlocks directly.
func TestInjectContextSystemMessageComposition(t *testing.T) {
	assert.Equal(t, "a\n\nb", operations.JoinLeadBlocks("a", "b"))
	assert.Equal(t, "b", operations.JoinLeadBlocks("", "b"))
	assert.Equal(t, "a", operations.JoinLeadBlocks("a", ""))
	assert.Empty(t, operations.JoinLeadBlocks("", ""))
}

// TestAgentSetupNudge_Wiring proves the SessionStart hook fires the nudge
// exactly when the project rooted at workDir has profiles but no agents, once
// (part<=1), and never blocks on a config it can't load.
func TestAgentSetupNudge_Wiring(t *testing.T) {
	// agentSetupNudge's config.Load is real-OS-fs (no config.WithFS): isolate
	// HOME so the home-layer read (D2/D3 layering) never reaches this
	// developer's real ~/.ctxloom — each subtest's writeRoot fixture must be
	// the only source of profiles/agents it's asserting on.
	testsupport.Isolate(t)
	t.Setenv(projectroot.EnvVar, "") // don't let an ambient root override workDir

	writeRoot := func(t *testing.T, body string) string {
		root := t.TempDir()
		appDir := filepath.Join(root, ".ctxloom")
		require.NoError(t, os.MkdirAll(appDir, 0755))
		require.NoError(t, os.WriteFile(filepath.Join(appDir, "config.yaml"), []byte(body), 0644))
		return root
	}

	t.Run("profiles, no agents → nudge on first chunk", func(t *testing.T) {
		root := writeRoot(t, "version: 6\nprofiles:\n  definitions:\n    default: {}\n")
		assert.NotEmpty(t, agentSetupNudge(root, 1))
		assert.Empty(t, agentSetupNudge(root, 2), "fires once, on the first chunk")
	})

	t.Run("agent configured → silent", func(t *testing.T) {
		root := writeRoot(t, "version: 6\nprofiles:\n  definitions:\n    default: {}\nagents:\n  dev:\n    profiles: [default]\n")
		assert.Empty(t, agentSetupNudge(root, 1))
	})

	t.Run("no .ctxloom → silent, never blocks", func(t *testing.T) {
		assert.Empty(t, agentSetupNudge(t.TempDir(), 1))
	})
}

// TestHookInjectContext_PanicIsNotSuccess pins that a PANICKING
// inject-context hook fails loud instead of reporting success with no context.
// The recovery's job is to keep the host from hanging (it still writes the
// empty `{}` envelope to stdout), not to convert a crash into a clean exit: a
// hook that panics has delivered zero context, and exit 0 makes that
// indistinguishable from "ctxloom had nothing to inject" — this project's
// signature silent-no-op shape.
//
// The panic is induced through the real production path with no test seam: the
// hook dereferences args[0], so an empty args slice panics inside the body the
// recovery covers. (Cobra's ExactArgs(1) makes that unreachable via the CLI;
// the point is that the recovery, whatever panics under it, must report.)
func TestHookInjectContext_PanicIsNotSuccess(t *testing.T) {
	var runErr error
	var stderr string
	stdout := captureStdout(t, func() {
		stderr = captureStderr(t, func() {
			runErr = hookInjectContextCmd.RunE(&cobra.Command{}, nil)
		})
	})

	require.Error(t, runErr,
		"a panicking hook must return an error so the process exits non-zero")
	assert.Equal(t, "{}\n", stdout,
		"the fallback empty envelope must still reach stdout so the host never hangs")
	assert.Contains(t, stderr, "panic:",
		"the panic must stay diagnosable on stderr")
}

// stdinFromString replaces os.Stdin with a real *os.File holding s for the
// duration of the test. The inject-context hook reads the host's payload
// straight off os.Stdin, so driving it end to end needs a file, not a Reader.
func stdinFromString(t *testing.T, s string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "stdin.json")
	require.NoError(t, os.WriteFile(path, []byte(s), 0o600))
	f, err := os.Open(path)
	require.NoError(t, err)
	orig := os.Stdin
	os.Stdin = f
	t.Cleanup(func() {
		os.Stdin = orig
		_ = f.Close()
	})
}

// TestHookInjectContext_MissingContextSkipsRendezvous pins that a
// chunk invocation with NOTHING to emit does not join the ordering rendezvous.
//
// When the context file is missing, ChunkContext("") yields no chunks, so every
// part is out of range and emits empty content — yet each part>1 still queued on
// agent.AwaitTurn, waiting the full ContextRendezvousTimeout for a predecessor
// marker that never appears. That is up to 5 s of session-startup latency spent
// ordering nothing. Ordering only matters when there IS a chunk to order.
//
// Asserted structurally (no rendezvous directory is created for this session)
// and by cost (the call returns far inside the timeout), plus the payload: the
// hook still emits the empty envelope.
func TestHookInjectContext_MissingContextSkipsRendezvous(t *testing.T) {
	sessionID := "u036f03-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	stdinFromString(t, `{"session_id":"`+sessionID+`","source":"startup"}`)

	origProject, origPart, origTotal := injectContextProject, injectContextPart, injectContextTotal
	injectContextProject, injectContextPart, injectContextTotal = t.TempDir(), 2, 2
	t.Cleanup(func() {
		injectContextProject, injectContextPart, injectContextTotal = origProject, origPart, origTotal
	})

	var warnings strings.Builder
	t.Cleanup(clidiag.SetSink(&warnings))

	var runErr error
	start := time.Now()
	out := captureStdout(t, func() {
		runErr = hookInjectContextCmd.RunE(&cobra.Command{}, []string{"nosuchcontexthash"})
	})
	elapsed := time.Since(start)

	require.NoError(t, runErr)
	assert.Equal(t, "{}\n", out, "nothing to inject must still emit the empty envelope")
	assert.Contains(t, warnings.String(), "failed to read context file",
		"the missing context file must stay diagnosable")
	assert.NoDirExists(t, filepath.Join(os.TempDir(), "ctxloom-rdv-"+sessionID),
		"a part with no chunk to emit must not join the ordering rendezvous")
	assert.Less(t, elapsed, agent.ContextRendezvousTimeout/2,
		"a part with no chunk to emit must not wait for a predecessor")
}
