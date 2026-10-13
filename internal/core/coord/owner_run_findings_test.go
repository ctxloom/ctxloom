package coord

import (
	"context"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// The owner run's outcome carries what its runner reported while executing
// the launch (task #18): the owning `ctxloom run` renders them, because the
// runner's own stderr surfaces only when a run fails. The findings cross the
// real wire — engine host, StartRun answer, codec — to get here.
//
// MUTATION: drop the findings from the RunOutcome (or from the run in
// issueStartRun) → red.
func TestStartOwnedRun_OutcomeCarriesWhatTheRunnerReported(t *testing.T) {
	sp := newFakeSpawner(t, nil, nil)
	c := newTestCoordinator(t, sp, nil)
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	const ownerHarp = "owner-harp-findings"
	token, err := c.RegisterSessionOwner(ownerHarp)
	require.NoError(t, err)
	owner, ok := c.Identify(token)
	require.True(t, ok)

	reported := report.Findings{{Text: "command /deploy skipped: its name collides with a built-in", Remedy: "rename the command"}}
	sc := &scriptedChat{}
	starter := func(_ context.Context, spawnEnv map[string]string) (OwnedRunner, error) {
		sctx, scancel := context.WithCancel(ctx)
		host := runnerHooks.NewEngineHost(sctx, nil, "claude-code", spawnEnv[EnvRunID])
		runnerHooks.BindReportingTestRunner(host, sc, reported)
		home, err := runnerHooks.NewHome(sctx, TestHomeConfig{
			Reporter: termSink(), URL: spawnEnv[EnvCoordURL], Token: spawnEnv[EnvCoordCred],
			RunID: spawnEnv[EnvRunID], Harness: "claude-code", Version: "test", Engine: host.Handle,
		})
		if err != nil {
			scancel()
			return OwnedRunner{}, err
		}
		host.BindHome(home)
		return OwnedRunner{Kill: func() { scancel(); home.Crash() }}, nil
	}

	outcome, err := c.StartOwnedRun(ctx, owner, ownerRun(ownerLaunch(ownerHarp, "claude-code", "fast", "sonnet", "/work", "bypass"), false), starter, "hello owner run")
	require.NoError(t, err)
	assert.Equal(t, reported, outcome.Findings)
}
