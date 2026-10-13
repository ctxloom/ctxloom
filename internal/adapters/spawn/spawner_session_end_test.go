package spawn

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// sessionLedger records the spawner's session-accounting calls: each liveness
// lock held, and each session end, by harp, in order.
type sessionLedger struct {
	mu    sync.Mutex
	held  []string
	ended []string
}

func (l *sessionLedger) hold(harp string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.held = append(l.held, harp)
	return nil
}

func (l *sessionLedger) end(harp string, _ time.Time) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.ended = append(l.ended, harp)
	return nil
}

func (l *sessionLedger) snapshot() (held, ended []string) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.held...), append([]string(nil), l.ended...)
}

// newLedgerSpawner is the production spawner with its session accounting
// recorded and its runner start stubbed: a Start that reaches the engine
// start fails there, after the spawner has done its own part.
func newLedgerSpawner(t *testing.T) (*spawner, *sessionLedger) {
	t.Helper()
	prev := startEngine
	startEngine = func(context.Context, launch.Launch, map[string]string, int, func(context.Context) (*isolation.RunnerHandle, error)) (*operations.EngineProcess, error) {
		return nil, errors.New("stub: no runner in this test")
	}
	t.Cleanup(func() { startEngine = prev })
	s := newSpawner(termRep(), nil, t.TempDir(), nil)
	l := &sessionLedger{}
	s.holdLock, s.endSession = l.hold, l.end
	return s, l
}

func startRunOf(t *testing.T, s *spawner, harp, runID string) {
	t.Helper()
	_, err := s.Start(context.Background(), launch.Launch{Identity: sessions.Identity{Harp: harp, RunID: runID}},
		sessions.Endpoint{URL: "u", Credential: "c"})
	require.Error(t, err, "premise: the stubbed runner start fails")
}

// TestProdSpawner_ALateEndOfAnEndedRunSparesItsResumedSuccessor: a run's end
// is recorded before its teardown, and MarkSessionEnded comes last in that
// teardown — so a resume can start the harp's next run first. The ended run's
// call then arrives for a session its successor now runs in: keyed by harp
// alone it stamped that session ended and released the liveness lock the
// successor runs under, and a reaper could take the live session's home. It
// must be a no-op; the successor's own end still ends it.
func TestProdSpawner_ALateEndOfAnEndedRunSparesItsResumedSuccessor(t *testing.T) {
	s, ledger := newLedgerSpawner(t)
	const harp = "late-end-harp"
	startRunOf(t, s, harp, "run-ended")
	startRunOf(t, s, harp, "run-resumed")

	s.MarkSessionEnded(harp, "run-ended")
	_, ended := ledger.snapshot()
	assert.Empty(t, ended, "the ended run's late call ended the session its resumed successor runs in")

	s.MarkSessionEnded(harp, "run-resumed")
	_, ended = ledger.snapshot()
	assert.Equal(t, []string{harp}, ended, "the run that holds the session ends it")
}

// TestProdSpawner_AResumedRunHoldsItsSessionsLivenessLock: the lock is taken
// when a session is minted (AssignSession) and released at its run's end, so
// the run a resume starts must take it again — or it runs with the session
// reading dead, its home reclaimable under it.
func TestProdSpawner_AResumedRunHoldsItsSessionsLivenessLock(t *testing.T) {
	s, ledger := newLedgerSpawner(t)
	const harp = "resumed-harp"
	startRunOf(t, s, harp, "run-1")
	s.MarkSessionEnded(harp, "run-1")
	startRunOf(t, s, harp, "run-2")

	held, ended := ledger.snapshot()
	assert.Equal(t, []string{harp}, ended)
	assert.Equal(t, []string{harp, harp}, held, "each run that starts holds its session's liveness lock")
}

// TestProdSpawner_AnEndBeforeAnyRunStartedStillEndsTheSession: a spawn refused
// before its run registered (releaseAssignedHarp, run id ""), and a run that
// failed before its runner was started, own the session AssignSession minted
// — nothing else will end it.
func TestProdSpawner_AnEndBeforeAnyRunStartedStillEndsTheSession(t *testing.T) {
	s, ledger := newLedgerSpawner(t)
	s.MarkSessionEnded("refused-harp", "")
	s.MarkSessionEnded("unstarted-harp", "run-never-started")
	_, ended := ledger.snapshot()
	assert.Equal(t, []string{"refused-harp", "unstarted-harp"}, ended)
}

// A session's accounting that fails is warned about, naming the harp; one
// that succeeds is not. Both are best-effort: the run goes on either way.
func TestProdSpawner_SessionAccountingWarnsOnlyWhenItFails(t *testing.T) {
	for name, fail := range map[string]bool{"succeeds": false, "fails": true} {
		t.Run(name, func(t *testing.T) {
			var findings report.Collector
			s := newSpawner(report.To(&findings), nil, t.TempDir(), nil)
			var err error
			if fail {
				err = errors.New("disk full")
			}
			s.holdLock = func(string) error { return err }
			s.endSession = func(string, time.Time) error { return err }

			s.claimSession("acct-harp", "run-1")
			s.MarkSessionEnded("acct-harp", "run-1")

			texts := findings.All().Texts()
			if !fail {
				assert.Empty(t, texts, "accounting that succeeded is not warned about")
				return
			}
			require.Len(t, texts, 2, "the lock and the end each warn: %q", texts)
			assert.Contains(t, texts[0], "cannot hold its session's liveness lock")
			assert.Contains(t, texts[1], "agent acct-harp: end session: disk full")
		})
	}
}
