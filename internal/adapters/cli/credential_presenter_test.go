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
	assert.Equal(t, "", credentialNoticeText(nil, ""))

	got := credentialNoticeText([]coord.CredentialHold{tokenLimitHold("a", "b")}, "")
	for _, want := range []string{"RATE LIMITED", "claude-code", "CLAUDE_CODE_OAUTH_TOKEN", "2 waiting", "on their own", limitUntil.Local().Format("15:04:05")} {
		assert.Contains(t, got, want)
	}
	for _, not := range []string{"restart", "sign in", "REFUSED"} {
		assert.NotContains(t, got, not, "a limit needs nothing from the human")
	}
	assert.Contains(t, credentialNoticeText([]coord.CredentialHold{loginLimitHold("a")}, ""), "~/.claude")
	assert.Contains(t, credentialNoticeText([]coord.CredentialHold{tokenLimitHold("a"), loginLimitHold("b")}, ""), "+1 more")
}

// An overload hold is one run's own backoff: the notice says the engine was
// overloaded, names that run and when it resumes, and names no credential —
// nothing about the credential is spent, and no sibling waits.
func TestCredentialNoticeText_OverloadedNamesTheRunNotACredential(t *testing.T) {
	src := engine.Credentials{Env: map[string]string{"CLAUDE_CODE_OAUTH_TOKEN": "v"}}.Source("claude-code")
	h := coord.CredentialHold{Engine: "claude-code", Source: src, Kind: agent.FailureOverloaded, Until: limitUntil, Harps: []string{"busy-kid"}}
	got := credentialNoticeText([]coord.CredentialHold{h}, "")
	for _, want := range []string{"OVERLOADED", "claude-code", "busy-kid", "on its own", limitUntil.Local().Format("15:04:05")} {
		assert.Contains(t, got, want)
	}
	for _, not := range []string{"RATE LIMITED", "CLAUDE_CODE_OAUTH_TOKEN", "waiting"} {
		assert.NotContains(t, got, not)
	}
}

func refusedHold(src engine.CredentialSource, since time.Time, harps ...string) coord.CredentialHold {
	return coord.CredentialHold{Engine: "claude-code", Source: src, Kind: agent.FailureCredentialRejected, Since: since, Harps: harps}
}

// A refused credential needs the human: the notice leads CREDENTIAL REFUSED,
// names the carrier and how many runs are parked, and gives the remedy for
// where the credential comes from — a captured variable needs a fresh export
// and a restart of THIS session; a store needs a sign-in, then a resume. It
// never claims the runs resume on their own.
func TestCredentialNoticeText_RefusedGivesTheRemedyForItsSource(t *testing.T) {
	token := tokenLimitHold().Source
	got := credentialNoticeText([]coord.CredentialHold{refusedHold(token, limitUntil, "a", "b")}, "root-harp")
	for _, want := range []string{"CREDENTIAL REFUSED", "claude-code", "CLAUDE_CODE_OAUTH_TOKEN", "2 parked",
		"export a fresh CLAUDE_CODE_OAUTH_TOKEN", "ctxloom run --session root-harp"} {
		assert.Contains(t, got, want)
	}
	assert.NotContains(t, got, "on their own")

	login := loginLimitHold().Source
	got = credentialNoticeText([]coord.CredentialHold{refusedHold(login, limitUntil, "a")}, "root-harp")
	assert.Contains(t, got, "sign in again")
	assert.Contains(t, got, "~/.claude")
	assert.NotContains(t, got, "--session")
}

// ringCounter counts the presenter's bells.
type ringCounter struct {
	mu sync.Mutex
	n  int
}

func (r *ringCounter) ring() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.n++
	return true
}

func (r *ringCounter) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.n
}

// A refused credential's hold rings the bell ONCE when it opens — and when a
// timed hold is upgraded to one — never on the refreshes that keep its notice
// up, and again only for another hold.
func TestCredentialPresenter_RingsOnceWhenARefusalHoldOpens(t *testing.T) {
	src := &holdSource{}
	bells := &ringCounter{}
	clk := fakeclock.New()
	p := &credentialPresenter{holds: src.get, noteBar: (&noteRecorder{}).noteBar, ring: bells.ring, clock: clk}
	p.refresh()

	token := tokenLimitHold().Source
	limited := tokenLimitHold("a", "b")
	limited.Since = clk.Now()
	src.set(limited)
	p.refresh()
	assert.Zero(t, bells.count(), "a rate limit needs nothing from the human")

	src.set(refusedHold(token, limited.Since, "a", "b"))
	p.refresh()
	assert.Equal(t, 1, bells.count(), "the upgrade to a refusal rings")
	p.refresh()
	clk.Advance(credentialNoteRefresh)
	p.refresh()
	assert.Equal(t, 1, bells.count(), "the notice's refreshes never ring")

	src.set()
	p.refresh()
	src.set(refusedHold(token, clk.Now(), "a"))
	p.refresh()
	assert.Equal(t, 2, bells.count(), "another hold rings once more")
}

// A refusal hold already in force at the first poll — rebuilt by a restart —
// does not ring: the restarted coordinator's finding tells the human again,
// and the bell is for a hold OPENING.
func TestCredentialPresenter_AHoldInForceAtStartDoesNotRing(t *testing.T) {
	src := &holdSource{}
	src.set(refusedHold(tokenLimitHold().Source, limitUntil, "a"))
	bells := &ringCounter{}
	rec := &noteRecorder{}
	p := &credentialPresenter{holds: src.get, noteBar: rec.noteBar, ring: bells.ring, clock: fakeclock.New()}
	p.refresh()
	assert.Zero(t, bells.count())
	require.Len(t, rec.all(), 1)
	assert.Contains(t, rec.all()[0], "CREDENTIAL REFUSED", "the notice is up all the same")
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
	p := &credentialPresenter{holds: src.get, noteBar: rec.noteBar, ring: func() bool { return true }, clock: clk}

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
		presentCredentialHolds(ctx, &credentialPresenter{holds: src.get, noteBar: rec.noteBar, ring: func() bool { return true }, clock: clk})
	}()
	t.Cleanup(func() { cancel(); <-done })
	require.Eventually(t, func() bool { return clk.Pending() > 0 }, 5*time.Second, time.Millisecond)

	src.set(tokenLimitHold("a"))
	clk.Advance(presenterTick)
	require.Eventually(t, func() bool { return len(rec.all()) == 1 }, 5*time.Second, time.Millisecond)
	assert.Contains(t, rec.all()[0], "on their own")
}
