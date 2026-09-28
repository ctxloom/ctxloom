package watch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStream_EmitsUpFrontThenOnARealChange pins the end-to-end path through a
// real fsnotify watcher: a subscriber sees current state immediately, and an
// actual write to a watched file reaches it. How MANY emits a burst produces is
// deliberately not asserted here — see the synctest test below for why.
func TestStream_EmitsUpFrontThenOnARealChange(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "log.jsonl")

	w, err := New(dir, false, func(p string) bool { return p == target })
	require.NoError(t, err)
	defer func() { _ = w.Close() }()

	var emits atomic.Int64
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- Stream(ctx, w, 30*time.Millisecond, func() error { emits.Add(1); return nil }) }()

	require.Eventually(t, func() bool { return emits.Load() == 1 }, 2*time.Second, 5*time.Millisecond,
		"Stream must emit once up front, before any change")

	require.NoError(t, os.WriteFile(target, []byte("x"), 0o644))
	require.Eventually(t, func() bool { return emits.Load() >= 2 }, 2*time.Second, 5*time.Millisecond,
		"a real change to a watched file must reach the subscriber")

	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err, "a cancelled context is a clean shutdown, not an error")
	case <-time.After(2 * time.Second):
		t.Fatal("Stream did not return after the context was cancelled")
	}
}

// TestStream_ABurstForOneChangeDebouncesToOneEmit pins the debounce: events
// arriving closer together than the debounce collapse into a single emit.
//
// This runs on synctest's fake clock and sends the events itself. Real
// filesystem writes cannot establish "these events were one burst": the
// debounce correctly treats any quiet gap longer than the interval as the
// writing having stopped, and a writer stalled in a syscall on a loaded CI
// runner produces exactly such a gap, so a wall-clock burst splits into
// several and the count flakes. Here the gaps are chosen, not observed.
func TestStream_ABurstForOneChangeDebouncesToOneEmit(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		w := &Watcher{
			events: make(chan Event),
			errs:   make(chan error),
			done:   make(chan struct{}),
		}
		const debounce = 30 * time.Millisecond

		var emits atomic.Int64
		ctx, cancel := context.WithCancel(t.Context())
		done := make(chan error, 1)
		go func() { done <- Stream(ctx, w, debounce, func() error { emits.Add(1); return nil }) }()

		synctest.Wait()
		require.Equal(t, int64(1), emits.Load(), "Stream must emit once up front, before any change")

		// One logical change, several events, each inside the debounce of the
		// last — together spanning well past a single debounce interval, so
		// only a timer that restarts on every event keeps them one burst.
		for range 5 {
			w.events <- Event{Path: "log.jsonl"}
			time.Sleep(debounce / 2)
			synctest.Wait()
			require.Equal(t, int64(1), emits.Load(), "an event inside the debounce window must not emit")
		}

		time.Sleep(debounce)
		synctest.Wait()
		require.Equal(t, int64(2), emits.Load(), "the burst must emit once when the writing stops")

		// Long past the ceiling too: a burst that has emitted must not emit
		// again on a timer left over from it.
		time.Sleep(2 * maxDebounceWait(debounce))
		synctest.Wait()
		assert.Equal(t, int64(2), emits.Load(), "a burst for one change must debounce, not emit per event")

		cancel()
		assert.NoError(t, <-done, "a cancelled context is a clean shutdown, not an error")
	})
}

// TestStream_ReturnsTheEmitError pins that a write failure on the output stream
// stops the watch instead of spinning forever emitting into a broken pipe.
func TestStream_ReturnsTheEmitError(t *testing.T) {
	dir := t.TempDir()
	w, err := New(dir, false, nil)
	require.NoError(t, err)
	defer func() { _ = w.Close() }()

	want := errors.New("broken pipe")
	got := Stream(context.Background(), w, time.Millisecond, func() error { return want })
	assert.ErrorIs(t, got, want)
}

// A long-lived stream command must not report a CLEAN shutdown when its
// watcher died underneath it. Stream treated a closed event channel as
// "return nil", so `taskloom watch` / `ctxloom plan watch` exited 0 with no
// diagnostic while a subscriber went on waiting for events that could never
// arrive again. Only a deliberate Close is a clean end.
func TestStream_WatcherDeathIsAnErrorNotACleanExit(t *testing.T) {
	dir := t.TempDir()
	w, err := New(dir, false, nil)
	require.NoError(t, err)
	defer func() { _ = w.Close() }()

	done := make(chan error, 1)
	go func() {
		done <- Stream(context.Background(), w, time.Millisecond, func() error { return nil })
	}()

	// Kill the UNDERLYING fsnotify watcher without going through w.Close(),
	// which is exactly what a watcher dying on its own looks like: the event
	// channel closes while nobody asked for a shutdown.
	require.NoError(t, w.fsw.Close())

	select {
	case err := <-done:
		require.Error(t, err, "a dead watcher reported as exit 0 is a false clean shutdown")
		assert.Contains(t, err.Error(), "watch", "the diagnostic must name what stopped")
	case <-time.After(2 * time.Second):
		t.Fatal("Stream did not return after its watcher died")
	}
}

// A deliberate Close is still a clean end: the caller asked for it, so the
// stream must not manufacture a failure out of its own shutdown.
func TestStream_DeliberateCloseIsStillACleanExit(t *testing.T) {
	dir := t.TempDir()
	w, err := New(dir, false, nil)
	require.NoError(t, err)

	done := make(chan error, 1)
	go func() {
		done <- Stream(context.Background(), w, time.Millisecond, func() error { return nil })
	}()

	require.NoError(t, w.Close())

	select {
	case err := <-done:
		assert.NoError(t, err, "a caller-requested Close is a clean shutdown")
	case <-time.After(2 * time.Second):
		t.Fatal("Stream did not return after Close")
	}
}

// A CONTINUOUS WRITER MUST NOT STARVE THE STREAM.
//
// The debounce timer was Reset on every event with no ceiling, so a source
// changing more often than the debounce interval pushed the emit deadline
// forward forever: the subscriber saw NOTHING for as long as the writing
// continued, which is exactly when it most needs to see something. A bounded
// maximum wait forces an emit mid-burst.
//
// Events are fed synthetically rather than through the filesystem: real writes
// deliver in bursts with gaps wide enough for the debounce to fire on its own,
// which would let this pass against the unfixed code. Feeding the channel
// directly guarantees the starvation condition actually holds.
func TestStream_SustainedEventsStillEmitWithinTheMaximumWait(t *testing.T) {
	w := &Watcher{
		events: make(chan Event),
		errs:   make(chan error),
		done:   make(chan struct{}),
	}

	const debounce = 50 * time.Millisecond
	ceiling := maxDebounceWait(debounce)

	var emits atomic.Int64
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- Stream(ctx, w, debounce, func() error { emits.Add(1); return nil }) }()

	require.Eventually(t, func() bool { return emits.Load() >= 1 }, 2*time.Second, 5*time.Millisecond,
		"Stream must emit once up front")
	base := emits.Load()

	// Feed events strictly faster than the debounce, without pause, so every
	// one of them Resets the timer and only a ceiling can produce an emit.
	stopFeed := make(chan struct{})
	fed := make(chan struct{})
	go func() {
		defer close(fed)
		tick := time.NewTicker(debounce / 5)
		defer tick.Stop()
		for {
			select {
			case <-stopFeed:
				return
			case <-tick.C:
				select {
				case w.events <- Event{Path: "busy.jsonl"}:
				case <-stopFeed:
					return
				}
			}
		}
	}()

	emitted := assert.Eventually(t, func() bool { return emits.Load() > base },
		ceiling+2*time.Second, 10*time.Millisecond,
		"an unbroken event stream starved the emit: with no maximum wait the debounce timer resets forever and the subscriber never hears anything")

	close(stopFeed)
	<-fed
	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err, "a cancelled context is a clean shutdown")
	case <-time.After(2 * time.Second):
		t.Fatal("Stream did not return after the context was cancelled")
	}
	require.True(t, emitted, "starvation confirmed above; failing here keeps the shutdown assertions meaningful")
}
