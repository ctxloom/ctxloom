package cli

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/testsupport/fakeclock"
)

var limitUntil = time.Date(2026, 10, 1, 17, 30, 5, 0, time.UTC)

func tokenLimitHold(harps ...string) coord.CredentialHold {
	src := engine.Credentials{Env: map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "v"}}.Source("claude-code")
	return coord.CredentialHold{Engine: "claude-code", Source: src, Kind: agent.FailureRateLimited, Until: limitUntil, Harps: harps}
}

func loginLimitHold(harps ...string) coord.CredentialHold {
	src := engine.Credentials{Stores: []engine.SharedStore{{Var: "CLAUDE_CONFIG_DIR", HomeRel: ".claude"}}}.Source("claude-code")
	return coord.CredentialHold{Engine: "claude-code", Source: src, Kind: agent.FailureRateLimited, Until: limitUntil, Harps: harps}
}

// A rate-limited hold waits on its own: the notice names the engine, the
// credential's carrier (a variable's name, or a store read in place) and how
// many runs wait, and says when they resume as an absolute time — a countdown
// would change every tick and clobber every other bar note. It asks the human
// for nothing.
func TestCredentialNoticeText_RateLimited(t *testing.T) {
	assert.Equal(t, "", credentialNoticeText(nil))

	got := credentialNoticeText([]coord.CredentialHold{tokenLimitHold("a", "b")})
	for _, want := range []string{"RATE LIMITED", "claude-code", "CLAUDE_CODE_OAUTH_TOKEN", "2 waiting", "on their own", limitUntil.Local().Format("15:04:05")} {
		assert.Contains(t, got, want)
	}
	for _, not := range []string{"restart", "sign in", "REFUSED"} {
		assert.NotContains(t, got, not, "a limit needs nothing from the human")
	}
	assert.Contains(t, credentialNoticeText([]coord.CredentialHold{loginLimitHold("a")}), "~/.claude")
	assert.Contains(t, credentialNoticeText([]coord.CredentialHold{tokenLimitHold("a"), loginLimitHold("b")}), "+1 more")
}

// An overload hold is one run's own backoff: the notice says the engine was
// overloaded, names that run and when it resumes, and names no credential —
// nothing about the credential is spent, and no sibling waits.
func TestCredentialNoticeText_OverloadedNamesTheRunNotACredential(t *testing.T) {
	src := engine.Credentials{Env: map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "v"}}.Source("claude-code")
	h := coord.CredentialHold{Engine: "claude-code", Source: src, Kind: agent.FailureOverloaded, Until: limitUntil, Harps: []string{"busy-kid"}}
	got := credentialNoticeText([]coord.CredentialHold{h})
	for _, want := range []string{"OVERLOADED", "claude-code", "busy-kid", "on its own", limitUntil.Local().Format("15:04:05")} {
		assert.Contains(t, got, want)
	}
	for _, not := range []string{"RATE LIMITED", "CLAUDE_CODE_OAUTH_TOKEN", "waiting"} {
		assert.NotContains(t, got, not)
	}
}

// noteRecorder records every NoteBar call.
type noteRecorder struct {
	mu    sync.Mutex
	notes []string
}

func (n *noteRecorder) noteBar(text string, _ time.Duration) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.notes = append(n.notes, text)
}

func (n *noteRecorder) all() []string {
	n.mu.Lock()
	defer n.mu.Unlock()
	return append([]string(nil), n.notes...)
}

// holdSource is a settable CredentialHolds.
type holdSource struct {
	mu    sync.Mutex
	holds []coord.CredentialHold
}

func (s *holdSource) set(h ...coord.CredentialHold) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.holds = h
}

func (s *holdSource) get() []coord.CredentialHold {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]coord.CredentialHold(nil), s.holds...)
}

// A hold puts its notice on the bar; it is not re-set while unchanged until
// it would expire, then re-set so it never lapses; its release clears it.
func TestCredentialPresenter_RefreshKeepsTheNoticeUpWhileHeld(t *testing.T) {
	src := &holdSource{}
	rec := &noteRecorder{}
	clk := fakeclock.New()
	p := &credentialPresenter{holds: src.get, noteBar: rec.noteBar, clock: clk}

	p.refresh()
	assert.Empty(t, rec.all(), "nothing held: the bar is left alone")

	src.set(tokenLimitHold("a"))
	p.refresh()
	require.Len(t, rec.all(), 1)
	assert.Contains(t, rec.all()[0], "RATE LIMITED")

	p.refresh()
	assert.Len(t, rec.all(), 1, "an unchanged notice is not re-set every tick (it would clobber other notes)")

	clk.Advance(credentialNoteRefresh)
	p.refresh()
	assert.Len(t, rec.all(), 2, "re-set before it can expire")

	src.set()
	p.refresh()
	assert.Equal(t, "", rec.all()[2], "the release clears the notice")
}

// The loop refreshes on the presenter's tick until ctx ends.
func TestPresentCredentialHolds_RefreshesOnTheTick(t *testing.T) {
	src := &holdSource{}
	rec := &noteRecorder{}
	clk := fakeclock.New()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		presentCredentialHolds(ctx, src.get, rec.noteBar, clk)
	}()
	t.Cleanup(func() { cancel(); <-done })
	require.Eventually(t, func() bool { return clk.Pending() > 0 }, 5*time.Second, time.Millisecond)

	src.set(tokenLimitHold("a"))
	clk.Advance(presenterTick)
	require.Eventually(t, func() bool { return len(rec.all()) == 1 }, 5*time.Second, time.Millisecond)
	assert.Contains(t, rec.all()[0], "on their own")
}
