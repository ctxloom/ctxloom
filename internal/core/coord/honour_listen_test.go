package coord

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// listenSpawner resolves every child into a cell that names listen, and
// counts the runner starts the coordinator issues.
type listenSpawner struct {
	*fakeSpawner
	listen present.Listen
	starts atomic.Int32
}

func (s *listenSpawner) ResolveLaunch(ctx context.Context, plan *SpawnPlan, start SpawnStart) (Resolved, error) {
	r, err := s.fakeSpawner.ResolveLaunch(ctx, plan, start)
	r.Launch.Cell.Listen = s.listen
	return r, err
}

func (s *listenSpawner) Start(ctx context.Context, l launch.Launch, reach sessions.Endpoint) (*EngineSpawn, error) {
	s.starts.Add(1)
	return s.fakeSpawner.Start(ctx, l, reach)
}

// TestRunChild_HonoursTheCellsListenBeforeTheRunnerStarts: the listener a
// resolved cell names is the coordinator's to open AFTER the cell is prepared
// and BEFORE the runner starts — a runner started first would dial a
// re-minted reach nothing answers. One the coordinator cannot open fails the
// run loudly, and no runner is started.
func TestRunChild_HonoursTheCellsListenBeforeTheRunnerStarts(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	// TEST-NET-1: never an address of this host, so the listen must fail.
	sp := &listenSpawner{
		fakeSpawner: newFakeSpawner(map[string]fakeAgent{"worker": {perm: "bypass", runtime: launch.RuntimeRootless}}, nil),
		listen:      present.Listen{Addr: "192.0.2.1"},
	}
	var mu sync.Mutex
	var findings []string
	c, err := New(Options{
		ProjectDir: t.TempDir(),
		StateDir:   t.TempDir(),
		Spawner:    sp,
		OwnerHarp:  ownerIdentity().Harp,
		Reporter: report.SinkFunc(func(f report.Finding) {
			mu.Lock()
			defer mu.Unlock()
			findings = append(findings, fmt.Sprint(f))
		}),
	})
	require.NoError(t, err)
	require.NoError(t, runnerHooks.Serve(c))
	t.Cleanup(c.Close)

	_, err = c.AgentRun(context.Background(), ownerIdentity(), "worker", "task", "", "")
	require.NoError(t, err, "the verb's cheap checks pass: the coordinator serves and the axis is known")

	require.Eventually(t, func() bool {
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(strings.Join(findings, "\n"), "no listener to dial home to")
	}, 10*time.Second, 20*time.Millisecond, "the unopenable listener fails the run loudly")
	assert.Zero(t, sp.starts.Load(), "no runner is started toward a listener that does not exist")
}
