package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestPrintStartSessionBanner_MinimalInfo covers the degraded case (startup
// must never block on a missing piece of the banner) — only a harp,
// nothing else resolved.
func TestPrintStartSessionBanner_MinimalInfo(t *testing.T) {
	var buf bytes.Buffer
	PrintStartSessionBanner(&buf, StartSessionInfo{Harp: "swift-amber-falcon"})
	out := buf.String()
	assert.Contains(t, out, "starting session swift-amber-falcon")
	assert.NotContains(t, out, "profiles:")
	assert.NotContains(t, out, "context:")
	assert.NotContains(t, out, "previous session:")
}

func TestPrintStartSessionBanner_FullInfo(t *testing.T) {
	var buf bytes.Buffer
	PrintStartSessionBanner(&buf, StartSessionInfo{
		Harp:      "swift-amber-falcon",
		Backend:   "claude-code",
		Label:     "claude-code",
		Profiles:  []string{"developer", "reviewer"},
		Fragments: []string{"coding-standards", "security"},
		Tokens:    4200,
	})
	out := buf.String()
	assert.True(t, strings.HasPrefix(out, "ctxloom: starting session swift-amber-falcon (claude-code)"))
	assert.Contains(t, out, "profiles: developer, reviewer")
	assert.Contains(t, out, "context: 2 fragment(s), ~4200 tokens")
	assert.NotContains(t, out, "resume", "the banner points at no in-session recovery: engines own that")
}

func TestPrintStartSessionBanner_TokensWithNoFragments(t *testing.T) {
	// e.g. an --agent bound run whose FragmentsLoaded wasn't populated but the
	// assembled context still has a size — the banner still shows something.
	var buf bytes.Buffer
	PrintStartSessionBanner(&buf, StartSessionInfo{Harp: "h", Tokens: 100})
	assert.Contains(t, buf.String(), "context: ~100 tokens")
}

// TestPrintStartSessionBanner_NamesUnsafeRoutes: a plan routing a surface
// onto the project tree is announced as unsafe before the engine spawns;
// a session-only plan says nothing.
func TestPrintStartSessionBanner_NamesUnsafeRoutes(t *testing.T) {
	var buf bytes.Buffer
	PrintStartSessionBanner(&buf, StartSessionInfo{Harp: "swift-amber-falcon", Unsafe: []string{"context → project-root"}})
	assert.Contains(t, buf.String(), "unsafe: context → project-root")

	buf.Reset()
	PrintStartSessionBanner(&buf, StartSessionInfo{Harp: "swift-amber-falcon"})
	assert.NotContains(t, buf.String(), "unsafe")
}
