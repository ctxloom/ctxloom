package cli

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/sync/singleflight"
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
