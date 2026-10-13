package cli

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/adapters/fsstore"
	"github.com/ctxloom/ctxloom/internal/adapters/operations"
	"github.com/ctxloom/ctxloom/internal/adapters/runner"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/engines"
	"github.com/ctxloom/ctxloom/internal/shared/report"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/testsupport/coordharness"
	"github.com/ctxloom/ctxloom/internal/testsupport/scriptedchat"
	"github.com/ctxloom/ctxloom/internal/testsupport/spooltest"
)

// reportingOwnerStarter stands the owner run's runner up in-process — an
// engine host whose launch reports found, dialing the coordinator home.
func reportingOwnerStarter(t *testing.T, found report.Findings) coord.OwnedRunStarter {
	return func(_ context.Context, env map[string]string) (coord.OwnedRunner, error) {
		sctx, cancel := context.WithCancel(context.Background())
		host := runner.NewEngineHost(sctx, coordharness.Sink(), "mock", env[sessions.EnvRunID])
		host.BindRunner(scriptedRunner{eh: host, inst: &scriptedchat.Chat{}, findings: found})
		home, err := runner.NewHome(sctx, runner.HomeConfig{
			Reporter: coordharness.Sink(), URL: env[sessions.EnvCoordURL], Token: env[sessions.EnvCoordCred],
			RunID: env[sessions.EnvRunID], Harness: "mock", Version: "test",
			Engine: func(req *agentcoordpb.RunnerRequest) *agentcoordpb.RunnerResponse { return host.Handle(req) },
		})
		if err != nil {
			cancel()
			return coord.OwnedRunner{}, err
		}
		host.BindHome(home)
		kill := func() { cancel(); host.Close(); home.Crash() }
		t.Cleanup(kill)
		return coord.OwnedRunner{Kill: kill}, nil
	}
}

// `ctxloom run` shows what its runner reported while delivering the launch
// on a run that STARTS (task #18) — a skipped command or skill, a failed
// sweep — because the runner's own stderr surfaces only when a run fails.
// Driven through the real startOwnedRun against the coordinator the session
// hosts, with the findings crossing the RunnerChannel.
//
// MUTATION: drop the relay call in startOwnedRun → red.
func TestStartOwnedRun_ShowsWhatTheRunnerReported(t *testing.T) {
	testsupport.Isolate(t)
	spooltest.TeeHome(t)
	var shown report.Collector
	src, err := operations.ComposeSources(operations.Compose{})
	require.NoError(t, err)
	cfgOwner, err := config.Open(context.Background(), src)
	require.NoError(t, err)
	t.Cleanup(SetAppForTesting(operations.OpenedApp(cfgOwner, operations.Handed{
		Engines: engines.Registry(), SessionClaims: fsstore.SessionClaims,
		ContainerRuntimes: testComposition().ContainerRuntimes, Reporter: &shown,
	})))

	sp := &liveChildSpawner{}
	prev := theComposition.NewCoordinator
	theComposition.NewCoordinator = func(app *operations.App, opts coord.Options) (*coord.Coordinator, error) {
		opts.Spawner = sp
		return prev(app, opts)
	}
	t.Cleanup(func() { theComposition.NewCoordinator = prev })

	const owner = "relay-owner"
	st := &runState{workDir: t.TempDir(), activeHarp: owner}
	teardown := st.hostCoordinator()
	t.Cleanup(teardown)
	require.NotNil(t, st.sessionCoord, "precondition: the session hosts a coordinator: %v", shown.All().Texts())

	resolved, err := sp.ResolveLaunch(context.Background(),
		&coord.SpawnPlan{Backend: "mock", Label: "fast", Permission: "plan"},
		coord.SpawnStart{Identity: sessions.Identity{Harp: owner}, Prompt: "hi"})
	require.NoError(t, err)

	skip := report.Finding{Text: "command /deploy skipped: its name collides with a built-in", Remedy: "rename the command"}
	sess, err := startOwnedRun(context.Background(), st.sessionCoord, ownedRunLaunch{Launch: resolved.Launch}, reportingOwnerStarter(t, report.Findings{skip}))
	require.NoError(t, err)
	t.Cleanup(sess.cancel)

	assert.Contains(t, shown.All(), skip, "the run's output names what the runner skipped, with its fix")
}

// A relayed finding renders as an advisory: the runner already acted on its
// severity, so relaying a fail-loudly one must not re-ledger it against this
// process's startup gate. Text, remedy and once survive.
func TestRelayRunnerFindings_RendersEachAsAnAdvisory(t *testing.T) {
	var got report.Collector
	relayRunnerFindings(&got, report.Findings{
		{Kind: report.KindApply, Text: "runner: sweep failed", Remedy: "a later run retries", Once: true},
	})
	require.Len(t, got.All(), 1)
	f := got.All()[0]
	assert.False(t, f.Fatal(), "relayed, not re-raised")
	assert.Equal(t, report.Finding{Text: "runner: sweep failed", Remedy: "a later run retries", Once: true}, f)
}
