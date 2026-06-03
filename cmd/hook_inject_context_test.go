package cmd

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

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
			"hook event name MUST be SessionStart — Claude/Gemini route by this string")
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
	t.Run("flag_wins", func(t *testing.T) {
		got := resolveInjectContextWorkDir("/explicit/project",
			func(string) (string, error) { return "/git/root", nil })
		assert.Equal(t, "/explicit/project", got,
			"--project flag must beat git-root discovery")
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
