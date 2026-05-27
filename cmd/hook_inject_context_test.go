package cmd

import (
	"errors"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestBuildInjectContextOutput covers the wrapping logic that surrounds
// ctxloom-assembled context before the LLM sees it on SessionStart.
// Three cases: empty content (returns empty HookOutput to avoid
// misleading the LLM), non-empty content (gets header + tags + footer),
// and the SessionStart event name being set.
func TestBuildInjectContextOutput(t *testing.T) {
	t.Run("empty_content_yields_empty_output", func(t *testing.T) {
		out := buildInjectContextOutput("")
		assert.Nil(t, out.HookSpecificOutput,
			"empty content must NOT surface an AdditionalContext field — "+
				"otherwise the LLM sees the 'ctxloom content loaded' header "+
				"with nothing in it")
	})

	t.Run("wraps_content_with_header_and_footer", func(t *testing.T) {
		out := buildInjectContextOutput("rust rules go here")
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
		out := buildInjectContextOutput("anything")
		require.NotNil(t, out.HookSpecificOutput)
		assert.Equal(t, "SessionStart", out.HookSpecificOutput.HookEventName,
			"hook event name MUST be SessionStart — Claude/Gemini route by this string")
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
