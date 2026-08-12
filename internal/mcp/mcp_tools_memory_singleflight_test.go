package mcp

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/singleflight"

	"github.com/ctxloom/ctxloom/internal/agentcoord/coord"
)

// TestSingleflightDistill_ConcurrentCallersShareOneDistillation is the
// doubled-cost regression. The coordinator builds a FRESH ctxServer per relayed
// call, so the dedupe group has to be shared across those instances or it
// dedupes nothing. When a caller's budget expires mid-distillation the host
// keeps working (its handler runs on the coordinator's base context), so a
// retry must JOIN the run already in flight rather than start a rival one that
// re-distills every chunk through the LLM a second time.
func TestSingleflightDistill_ConcurrentCallersShareOneDistillation(t *testing.T) {
	group := &singleflight.Group{}
	serverFor := func() *ctxServer { return &ctxServer{distill: group} } // as coordCustomHandlers does

	var runs atomic.Int64
	release := make(chan struct{})
	entered := make(chan struct{})
	distill := func() (*loadSessionResult, error) {
		runs.Add(1)
		close(entered)
		<-release // hold the flight open, so a caller arriving now must join it
		return &loadSessionResult{Loaded: true, SessionID: "session-x"}, nil
	}

	results := make([]*loadSessionResult, 2)
	errs := make([]error, 2)
	var wg sync.WaitGroup

	// Caller 1 opens the flight and parks inside it.
	wg.Add(1)
	go func() {
		defer wg.Done()
		results[0], errs[0] = serverFor().singleflightDistill("session-x", distill)
	}()
	<-entered

	// Caller 2 — the retry — arrives while that flight is open. It signals
	// immediately before the call, then needs a moment to actually reach the
	// group's lock; releasing too early would let caller 1's flight finish
	// first and caller 2 would legitimately open a second one. The grace period
	// buys that arrival. It cannot mask a broken dedupe: a group that fails to
	// share simply runs the distillation twice and trips the assertion below.
	calling := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		close(calling)
		results[1], errs[1] = serverFor().singleflightDistill("session-x", distill)
	}()
	<-calling
	time.Sleep(100 * time.Millisecond)

	close(release)
	wg.Wait()

	assert.EqualValues(t, 1, runs.Load(),
		"a concurrent retry must join the distillation in flight, not start a second one")
	for i := range results {
		require.NoError(t, errs[i])
		require.NotNil(t, results[i])
		assert.Equal(t, "session-x", results[i].SessionID, "both callers get the shared result")
	}
}

// TestSingleflightDistill_DistinctSessionsDoNotBlockEachOther: the dedupe is
// keyed by session, not a global lock — two different sessions still distill
// concurrently.
func TestSingleflightDistill_DistinctSessionsDoNotBlockEachOther(t *testing.T) {
	s := &ctxServer{distill: &singleflight.Group{}}

	var runs atomic.Int64
	both := make(chan struct{})
	var wg sync.WaitGroup
	for _, key := range []string{"session-a", "session-b"} {
		wg.Add(1)
		go func(key string) {
			defer wg.Done()
			_, _ = s.singleflightDistill(key, func() (*loadSessionResult, error) {
				if runs.Add(1) == 2 {
					close(both) // both are in flight at once
				}
				<-both
				return &loadSessionResult{Loaded: true, SessionID: key}, nil
			})
		}(key)
	}
	wg.Wait() // deadlocks (and fails the test by timeout) if the two serialize

	assert.EqualValues(t, 2, runs.Load(), "distinct sessions distill independently")
}

// TestSingleflightDistill_NilGroupStillRuns: a bare ctxServer (the stdio
// fallback, and every test that builds one) has no group; the work must still
// happen, just undeduped.
func TestSingleflightDistill_NilGroupStillRuns(t *testing.T) {
	s := &ctxServer{}
	got, err := s.singleflightDistill("session-x", func() (*loadSessionResult, error) {
		return &loadSessionResult{Loaded: true, SessionID: "session-x"}, nil
	})
	require.NoError(t, err)
	require.NotNil(t, got)
	assert.True(t, got.Loaded)
}

// WHY compact_session CANNOT SIMPLY BE KEYED THE WAY THE OTHER PATHS ARE.
//
// handleCompactSession bypasses singleflightDistill, so an explicit
// compact_session does run a second full distillation alongside an in-flight
// recover_session/load_session of the same session. The obvious remedy —
// key it like distillSession does, on sessionID\x00backend\x00model — is
// unsound, and this pins why so nobody ships it.
//
// compact_session's session_id is OPTIONAL: omitted, it means "the caller's
// current session", and the compactor resolves that from the caller's own
// identity. The key would therefore be empty\x00backend\x00model for EVERY
// default-target call. On the coordinator the group is shared across all
// callers, so two different agents compacting their own separate sessions
// would collapse into one flight and both receive whichever session happened
// to win — silently distilling the wrong transcript into the wrong harp.
//
// Any real fix has to key on the CALLER's identity (or on an already-resolved
// session id), and must also decide whether a tool documented as the way to
// FORCE an essence may be served a result from a distillation it did not
// start. That is a semantics call, not a refactor.
func TestSingleflightDistill_UnresolvedSessionKeyCollapsesDifferentCallers(t *testing.T) {
	group := &singleflight.Group{}

	var runs atomic.Int64
	release := make(chan struct{})
	entered := make(chan struct{})
	body := func(harp string) func() (*loadSessionResult, error) {
		return func() (*loadSessionResult, error) {
			if runs.Add(1) == 1 {
				close(entered)
			}
			<-release
			return &loadSessionResult{Loaded: true, SessionID: "resolved-for-" + harp}, nil
		}
	}

	// The key a naive port of distillSession's scheme would build for a
	// compact_session that named no session_id: identical for both callers,
	// because the session id is not resolved yet.
	const naiveKey = "" + "\x00claude-code\x00haiku"

	results := make([]*loadSessionResult, 2)
	var wg sync.WaitGroup

	wg.Add(1)
	go func() {
		defer wg.Done()
		s := &ctxServer{distill: group, self: coord.Identity{Harp: "alpha-harp"}}
		results[0], _ = s.singleflightDistill(naiveKey, body("alpha-harp"))
	}()
	<-entered

	calling := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		close(calling)
		s := &ctxServer{distill: group, self: coord.Identity{Harp: "beta-harp"}}
		results[1], _ = s.singleflightDistill(naiveKey, body("beta-harp"))
	}()
	<-calling
	time.Sleep(50 * time.Millisecond) // let caller 2 reach the group's lock

	close(release)
	wg.Wait()

	require.EqualValues(t, 1, runs.Load(),
		"both callers shared one flight — which is the hazard: they are different sessions")
	assert.Equal(t, results[0].SessionID, results[1].SessionID,
		"beta-harp received alpha-harp's distillation; an unresolved session id must never be part of a shared key")
}
