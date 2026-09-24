package spawn_test

import (
	"context"
	"os"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/spawn"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
)

// fakeRuntime prepares, then waits for attach or cancellation. It records
// whether what it created was removed and whether the runner is alive.
type fakeRuntime struct {
	attach   chan struct{}
	created  bool
	removed  bool
	alive    bool
	killDoor bool
	env      map[string]string
}

func (f *fakeRuntime) Start(ctx context.Context, l launch.Launch, env map[string]string) (coord.RunnerHandle, error) {
	f.env = env
	f.created = true // the container / worktree / pty exists from here
	select {
	case <-ctx.Done():
		f.removed = true // abort the prepare: nothing is left behind
		return coord.RunnerHandle{}, ctx.Err()
	case <-f.attach:
	}
	f.alive = true // attached: ownership is the run record's now
	return coord.RunnerHandle{Name: "runner", Kill: func() { f.killDoor = true; f.alive = false }, Wait: func() error { return nil }}, nil
}

// TestStartRunner_CancelBeforeAttach_AbortsAndRemovesWhatItCreated pins the
// first half of the context contract.
func TestStartRunner_CancelBeforeAttach_AbortsAndRemovesWhatItCreated(t *testing.T) {
	rt := &fakeRuntime{attach: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := spawn.StartRunner(ctx, rt, launch.Launch{Identity: sessions.Identity{Harp: "h", RunID: "r"}}, sessions.Endpoint{URL: "u", Credential: "c"})
		done <- err
	}()
	cancel()
	require.ErrorIs(t, <-done, context.Canceled)
	require.True(t, rt.created)
	require.True(t, rt.removed, "a cancelled prepare leaves no orphan")
	require.False(t, rt.alive)
}

// TestStartRunner_CancelAfterAttach_IsIgnored_TeardownHasOneDoor pins the
// second half: after attach the ctx is not the teardown handle; only the
// handle's door ends the runner.
func TestStartRunner_CancelAfterAttach_IsIgnored_TeardownHasOneDoor(t *testing.T) {
	rt := &fakeRuntime{attach: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	close(rt.attach) // attach completes immediately
	h, err := spawn.StartRunner(ctx, rt, launch.Launch{Identity: sessions.Identity{Harp: "h", RunID: "r"}}, sessions.Endpoint{URL: "u", Credential: "c"})
	require.NoError(t, err)
	cancel()
	require.True(t, rt.alive, "cancelling the launch context after attach must not tear the runner down")
	require.False(t, rt.killDoor)
	h.Kill() // the one door
	require.False(t, rt.alive)
	require.True(t, rt.killDoor)
}

// TestStartRunner_ForwardsTheOwnerLossWindowOverride: the operator sets the
// runner's owner-loss window where they run ctxloom; a container runner
// inherits none of that environment, so the override rides the runner's env
// beside the reach-back — and is absent when the operator set nothing.
func TestStartRunner_ForwardsTheOwnerLossWindowOverride(t *testing.T) {
	start := func() map[string]string {
		rt := &fakeRuntime{attach: make(chan struct{})}
		close(rt.attach)
		_, err := spawn.StartRunner(context.Background(), rt, launch.Launch{Identity: sessions.Identity{Harp: "h", RunID: "r"}}, sessions.Endpoint{URL: "u", Credential: "c"})
		require.NoError(t, err)
		return rt.env
	}
	t.Setenv(sessions.EnvRunnerOwnerLossWindow, "5m")
	require.Equal(t, "5m", start()[sessions.EnvRunnerOwnerLossWindow])

	require.NoError(t, os.Unsetenv(sessions.EnvRunnerOwnerLossWindow))
	_, present := start()[sessions.EnvRunnerOwnerLossWindow]
	require.False(t, present, "nothing set, nothing forwarded")
}
