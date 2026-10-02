package runner

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// envWake is a WakeSpec that binds from one variable of the env it is handed.
type envWake struct{ key string }

type boundEnvWake struct{ at string }

func (boundEnvWake) Fire(context.Context, string) error { return nil }

func (s envWake) Bind(_ context.Context, env engine.WakeEnv) (engine.Wake, error) {
	v, ok := env(s.key)
	if !ok {
		return nil, fmt.Errorf("%w: no %s", engine.ErrWakeUnbound, s.key)
	}
	return boundEnvWake{at: v}, nil
}

// failingWake refuses its bind for a reason that is not a missing variable.
type failingWake struct{}

var errWakeBroken = errors.New("the wake socket path is not absolute")

func (failingWake) Bind(context.Context, engine.WakeEnv) (engine.Wake, error) {
	return nil, errWakeBroken
}

// driveOwner drives an interactive launch whose engine declares wake, with
// exec env env, and returns the fake home, the terminal and what was warned.
func driveOwner(t *testing.T, wake engine.Declared[engine.WakeSpec], env map[string]string) (*fakeEngineHome, *fakeTerminal, *report.Findings) {
	t.Helper()
	found := &report.Findings{}
	home := &fakeEngineHome{}
	term := newFakeTerminal(0, nil)
	eh := NewEngineHost(context.Background(), found, "claude-code", "run-1")
	eh.BindRunner(testRunner{eh: eh})
	t.Cleanup(eh.Close)
	eh.BindHome(home)
	eh.BindTerminal(term)
	turn := Turn{Launch: interactiveLaunch("harp-w", t.TempDir()), Wake: wake, Exec: engine.Exec{Env: env}}
	require.NoError(t, eh.Drive(context.Background(), turn))
	return home, term, found
}

// TestDrive_TheOwnersWakeBindsFromTheEnginesLaunchEnv: an interactive drive is
// the session owner's; the engine's declared wake binds from the env the
// engine is launched with, is registered for the owner, and is released when
// the engine exits.
// MUTATION — skip SetWake, or bind from the runner's own environment instead
// of the exec env — turns this red.
func TestDrive_TheOwnersWakeBindsFromTheEnginesLaunchEnv(t *testing.T) {
	home, term, found := driveOwner(t, engine.Provide[engine.WakeSpec](envWake{key: "WAKE_AT"}), map[string]string{"WAKE_AT": "/tmp/w.sock"})

	home.mu.Lock()
	assert.True(t, home.owner, "the interactive run is the session owner's")
	require.Len(t, home.wakes, 1)
	assert.Equal(t, boundEnvWake{at: "/tmp/w.sock"}, home.wakes[0])
	assert.Zero(t, home.released, "bound for as long as the engine runs")
	home.mu.Unlock()
	assert.Empty(t, *found)

	close(term.release)
	require.Eventually(t, func() bool {
		home.mu.Lock()
		defer home.mu.Unlock()
		return home.released == 1 && len(home.exited) == 1
	}, conformanceWait, 10*time.Millisecond, "the engine's exit releases its wake")
}

// TestDrive_AWakeThatBindsElsewhereIsNotAFault: an engine whose wake binds in
// a process it spawns itself (claude's relay) leaves nothing to bind in its
// launch env — no registration here, and nothing said.
func TestDrive_AWakeThatBindsElsewhereIsNotAFault(t *testing.T) {
	home, _, found := driveOwner(t, engine.Provide[engine.WakeSpec](envWake{key: "WAKE_AT"}), map[string]string{})
	home.mu.Lock()
	defer home.mu.Unlock()
	assert.True(t, home.owner)
	assert.Empty(t, home.wakes)
	assert.Empty(t, *found)
}

// TestDrive_AnAbsentOrBrokenWakeIsSaid: an engine that declares no wake, or
// whose wake fails to bind for another reason, leaves its owner's mail for
// the next prompt — and says so, with the reason.
func TestDrive_AnAbsentOrBrokenWakeIsSaid(t *testing.T) {
	_, _, found := driveOwner(t, engine.Absent[engine.WakeSpec]("this engine has no way in"), nil)
	require.Len(t, *found, 1)
	assert.Contains(t, (*found)[0].Text, "this engine has no way in")

	_, _, found = driveOwner(t, engine.Provide[engine.WakeSpec](failingWake{}), nil)
	require.Len(t, *found, 1)
	assert.Contains(t, (*found)[0].Text, errWakeBroken.Error())
}
