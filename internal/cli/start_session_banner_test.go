package cli

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/ctxloom/ctxloom/internal/operations"
)

// TestPrintStartSessionBanner_MinimalInfo covers the degraded case (WS-4/5:
// startup must never block on a missing piece of the banner) — only a harp,
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
		Previous:  &operations.PreviousSessionRef{Harp: "quiet-loyal-otter", Backend: "claude-code"},
	})
	out := buf.String()
	assert.True(t, strings.HasPrefix(out, "ctxloom: starting session swift-amber-falcon (claude-code)"))
	assert.Contains(t, out, "profiles: developer, reviewer")
	assert.Contains(t, out, "context: 2 fragment(s), ~4200 tokens")
	assert.Contains(t, out, "previous session: quiet-loyal-otter")
	assert.Contains(t, out, "resume")
}

func TestPrintStartSessionBanner_NoPreviousSession(t *testing.T) {
	var buf bytes.Buffer
	PrintStartSessionBanner(&buf, StartSessionInfo{Harp: "first-run-harp", Label: "claude-code"})
	assert.NotContains(t, buf.String(), "previous session:")
}

func TestPrintStartSessionBanner_TokensWithNoFragments(t *testing.T) {
	// e.g. an --agent bound run whose FragmentsLoaded wasn't populated but the
	// assembled context still has a size — the banner still shows something.
	var buf bytes.Buffer
	PrintStartSessionBanner(&buf, StartSessionInfo{Harp: "h", Tokens: 100})
	assert.Contains(t, buf.String(), "context: ~100 tokens")
}
