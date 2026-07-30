package watch

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestStream_EmitsOnceUpFrontThenPerDebouncedBurst pins the contract both
// watch commands need: a subscriber sees current state immediately, and a
// burst of filesystem events for one logical change collapses into one emit.
func TestStream_EmitsOnceUpFrontThenPerDebouncedBurst(t *testing.T) {
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

	// One logical append, several filesystem events.
	for range 5 {
		require.NoError(t, os.WriteFile(target, []byte("x"), 0o644))
	}
	require.Eventually(t, func() bool { return emits.Load() >= 2 }, 2*time.Second, 5*time.Millisecond)
	time.Sleep(200 * time.Millisecond)
	assert.LessOrEqual(t, emits.Load(), int64(3), "a burst for one change must debounce, not emit per event")

	cancel()
	select {
	case err := <-done:
		assert.NoError(t, err, "a cancelled context is a clean shutdown, not an error")
	case <-time.After(2 * time.Second):
		t.Fatal("Stream did not return after the context was cancelled")
	}
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
