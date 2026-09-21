package runner

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/testsupport/scriptedchat"
)

// mainEnv is a process environment double: what Main reads the reach-back
// from and scrubs it out of.
type mainEnv struct {
	vars    map[string]string
	unsets  []string
	refuse  string // a key whose unset fails
	refused error
}

func (e *mainEnv) getenv(k string) string { return e.vars[k] }
func (e *mainEnv) unsetenv(k string) error {
	e.unsets = append(e.unsets, k)
	if k == e.refuse {
		return e.refused
	}
	delete(e.vars, k)
	return nil
}

func reachEnv(url, cred, runID string) map[string]string {
	env := sessions.EncodeReach(sessions.Endpoint{URL: url, Credential: cred}, runID)
	return env
}

func mainDeps(env *mainEnv, ports func(*EngineHost, *Home) (Deps, error)) MainDeps {
	return MainDeps{
		Reporter: termSink(),
		Harness:  "mock",
		Version:  "test",
		Backend:  &scriptedchat.Chat{},
		Getenv:   env.getenv,
		Unsetenv: env.unsetenv,
		Ports:    ports,
	}
}

// TestMain_RefusesWithoutAReachBack: a runner is spawned FOR a run by an
// originator that stamped the reach-back trio on its environment; started by
// hand with none, there is nothing to dial and nothing to host, and it says
// so rather than standing an engine up over nothing.
func TestMain_RefusesWithoutAReachBack(t *testing.T) {
	env := &mainEnv{vars: map[string]string{}}
	err := Main(context.Background(), mainDeps(env, nil))
	require.ErrorIs(t, err, sessions.ErrNoReachBack)
}

// TestMain_RefusesAReachBackWithNoRun: the trio without a run id was the
// plugin-hosted owner arm's shape (a Home that hosted no run, its harp on
// the env). That arm is gone: every runner hosts exactly one run, whose
// identity arrives on the Launch. A runless trio is refused by name.
func TestMain_RefusesAReachBackWithNoRun(t *testing.T) {
	env := &mainEnv{vars: reachEnv("http://127.0.0.1:1", "cred", "")}
	err := Main(context.Background(), mainDeps(env, nil))
	require.ErrorIs(t, err, ErrNoRun)
}

// TestMain_ScrubsTheReachBackBeforeComposing: the runner is the ONE
// credential holder; the engine and every subprocess inherit this process's
// environment, so the trio is gone from it before the ports (and through them
// the engine) are composed. An unscrubbable key is fatal, not a warning.
func TestMain_ScrubsTheReachBackBeforeComposing(t *testing.T) {
	c := newTestCoordinator(t, newFakeSpawner(nil, nil), nil)
	token, err := c.RegisterSessionOwner(ownerIdentity().Harp)
	require.NoError(t, err)

	t.Run("scrubbed before Ports", func(t *testing.T) {
		env := &mainEnv{vars: reachEnv(c.LoopbackURL(), token, "run-1")}
		ctx, cancel := context.WithCancel(context.Background())
		var seen map[string]string
		ports := func(host *EngineHost, home *Home) (Deps, error) {
			seen = map[string]string{}
			for _, k := range []string{sessions.EnvCoordURL, sessions.EnvCoordCred, sessions.EnvRunID} {
				seen[k] = env.getenv(k)
			}
			cancel() // the runner has composed; end it
			return Deps{}, nil
		}
		_ = Main(ctx, mainDeps(env, ports))
		require.NotNil(t, seen, "Ports must have been composed")
		for k, v := range seen {
			assert.Empty(t, v, "%s must be scrubbed before the ports are composed", k)
		}
		assert.ElementsMatch(t, []string{sessions.EnvCoordURL, sessions.EnvCoordCred, sessions.EnvRunID}, env.unsets)
	})

	t.Run("an unscrubbable key is fatal", func(t *testing.T) {
		refused := errors.New("EPERM")
		env := &mainEnv{vars: reachEnv(c.LoopbackURL(), token, "run-1"), refuse: sessions.EnvCoordCred, refused: refused}
		composed := false
		ports := func(*EngineHost, *Home) (Deps, error) { composed = true; return Deps{}, nil }
		err := Main(context.Background(), mainDeps(env, ports))
		require.ErrorIs(t, err, ErrUnscrubbed)
		assert.ErrorIs(t, err, refused)
		assert.False(t, composed, "a runner whose environment still carries the credential must not compose an engine")
	})
}

// TestMain_DialsHomeThenBlocksUntilTheContextEnds: Main stands the runner up
// — the engine host for its one run, the dial-home, the ports over both —
// and then BLOCKS: the coordinator tears a runner down (RunnerHandle.Kill),
// or the process's own context ends it. A hosted runner that returned on
// its own would take its endpoint and its engine with it.
func TestMain_DialsHomeThenBlocksUntilTheContextEnds(t *testing.T) {
	c := newTestCoordinator(t, newFakeSpawner(nil, nil), nil)
	token, err := c.RegisterSessionOwner(ownerIdentity().Harp)
	require.NoError(t, err)

	env := &mainEnv{vars: reachEnv(c.LoopbackURL(), token, "run-1")}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	composed := make(chan struct{})
	var gotHost *EngineHost
	var gotHome *Home
	ports := func(host *EngineHost, home *Home) (Deps, error) {
		gotHost, gotHome = host, home
		close(composed)
		return Deps{}, nil
	}
	done := make(chan error, 1)
	go func() { done <- Main(ctx, mainDeps(env, ports)) }()

	select {
	case <-composed:
	case <-time.After(conformanceWait):
		t.Fatal("Main never composed its ports")
	}
	require.NotNil(t, gotHost, "the engine host for the one run")
	require.NotNil(t, gotHome, "the dialed home")
	select {
	case err := <-done:
		t.Fatalf("Main returned (%v) while its context was live — a runner blocks until torn down", err)
	case <-time.After(200 * time.Millisecond):
	}
	cancel()
	select {
	case err := <-done:
		require.NoError(t, err)
	case <-time.After(conformanceWait):
		t.Fatal("Main did not return once its context ended")
	}
}
