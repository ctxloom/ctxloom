package coord

import (
	"bytes"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/pkg/clifmt/clidiag"
)

// lockedBuffer is a goroutine-safe capture of the diagnostic stream: the
// drain warns from its own goroutine.
type lockedBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (l *lockedBuffer) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.Write(p)
}

func (l *lockedBuffer) String() string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.b.String()
}

// captureDiagnostics routes the process's user-facing warnings (the stream
// termSink renders onto) into a buffer for the rest of the test.
func captureDiagnostics(t *testing.T) *lockedBuffer {
	t.Helper()
	var buf lockedBuffer
	t.Cleanup(clidiag.SetSink(&buf))
	return &buf
}

// TestBeginDrain_AnnouncesTheWaitOnceWhenARunIsStillInATurn: a shutdown drain
// that has a run mid-turn to wait for says so, once, on the user-facing
// warning stream — naming the run, the bound and how to stop waiting — so a
// session whose own run has ended does not sit silent for up to the bound.
func TestBeginDrain_AnnouncesTheWaitOnceWhenARunIsStillInATurn(t *testing.T) {
	resetStrictness(t)
	gate := make(chan struct{}) // never closed: the turn holds the drain to its bound
	sp := newFakeSpawner(t, map[string]fakeAgent{"worker": {perm: "plan"}},
		func() *scriptedChat { return &scriptedChat{Gate: gate} })
	c := newTestCoordinator(t, sp, nil)
	c.drainBound = 300 * time.Millisecond

	harp := spawnGatedChild(t, sp, c)
	diag := captureDiagnostics(t)

	awaitDrain(t, c.BeginDrain())

	want := fmt.Sprintf(drainWaitNotice, shutdownPolicy().label, c.drainBound, 1, harp)
	require.Contains(t, diag.String(), want, "the drain must announce what it waits for")
	assert.Equal(t, 1, strings.Count(diag.String(), want), "the wait is announced once, not per pass")
}

// TestBeginDrain_SaysNothingWhenNoRunIsPending: a drain with nothing in a turn
// — here a child between turns, which ends at drain start — settles without
// announcing a wait.
func TestBeginDrain_SaysNothingWhenNoRunIsPending(t *testing.T) {
	resetStrictness(t)
	sp := newFakeSpawner(t, map[string]fakeAgent{"worker": {perm: "plan"}}, nil)
	c := newTestCoordinator(t, sp, nil)
	c.drainBound = time.Minute

	idle := spawnOneChild(t, c)
	require.Eventually(t, func() bool { return rosterState(c, idle) == StateIdle }, conformanceWait, 5*time.Millisecond)
	diag := captureDiagnostics(t)

	out := awaitDrain(t, c.BeginDrain())

	require.Equal(t, []string{idle}, out.Exited, "precondition: the idle child was tracked and ended at drain start")
	// The notice's fixed wording between the label and the bound, taken from
	// the constant so a reworded notice cannot leave this check vacuous.
	waiting, _, _ := strings.Cut(drainWaitNotice, "%s for")
	_, waiting, _ = strings.Cut(waiting, ": ")
	require.NotEmpty(t, strings.TrimSpace(waiting), "the notice's fixed wording must be found")
	assert.NotContains(t, diag.String(), waiting, "nothing pending, nothing announced")
}
