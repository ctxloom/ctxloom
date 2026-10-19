package coord

import (
	"context"
	"encoding/json"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/launch"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/shared/report"
)

// lifetimeAddr is a non-loopback-listener stand-in: 127.0.0.2 answers on
// Linux's lo, so a test can open and probe it without a LAN.
func lifetimeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.2:0")
	if err != nil {
		t.Skipf("127.0.0.2 is not bindable on this host: %v", err)
	}
	_ = ln.Close()
	return "127.0.0.2"
}

// recordedListenAddrs is endpoint.json's listen_addrs, and only that: the file
// also carries a credential this never reads out.
func recordedListenAddrs(t *testing.T, stateDir string) []string {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(stateDir, "endpoint.json"))
	require.NoError(t, err)
	var ep struct {
		ListenAddrs []string `json:"listen_addrs"`
	}
	require.NoError(t, json.Unmarshal(raw, &ep))
	return ep.ListenAddrs
}

// answers reports whether addr accepts a connection on c's loopback port.
func answers(t *testing.T, c *Coordinator, addr string) bool {
	t.Helper()
	u, err := url.Parse(c.LoopbackURL())
	require.NoError(t, err)
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(addr, u.Port()), time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func lifetimeCoordinator(t *testing.T, stateDir, addr string) *Coordinator {
	t.Helper()
	sp := &listenSpawner{
		fakeSpawner: newFakeSpawner(t, map[string]fakeAgent{"worker": {perm: "bypass", runtime: launch.RuntimeRootless}}, nil),
		listen:      present.Listen{Addr: addr},
	}
	c, err := New(Options{
		ProjectDir: t.TempDir(),
		StateDir:   stateDir,
		Spawner:    sp,
		OwnerHarp:  ownerIdentity().Harp,
		Reporter:   report.SinkFunc(func(report.Finding) {}),
	})
	require.NoError(t, err)
	require.NoError(t, runnerHooks.Serve(c))
	return c
}

// TestListener_LivesOnlyWhileAContainerRunNeedsIt: the listener beyond
// loopback a container cell named opens for the run and closes when the run
// ends, and endpoint.json stops listing it — a LAN-visible port held for no
// container is exposure for nothing.
func TestListener_LivesOnlyWhileAContainerRunNeedsIt(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	addr := lifetimeAddr(t)
	stateDir := t.TempDir()
	c := lifetimeCoordinator(t, stateDir, addr)
	t.Cleanup(c.Close)

	out, err := c.AgentRun(context.Background(), ownerIdentity(), "worker", "task", "")
	require.NoError(t, err)
	// The listener opens after the run is enqueued and before its runner
	// starts (TestRunChild_HonoursTheCellsListenBeforeTheRunnerStarts), so a
	// child that is UP proves Listen returned — and Listen records the address
	// before returning. Dialling the port proves only that it is bound, which
	// precedes the record: polling the dial raced the endpoint write.
	upCtx, cancel := context.WithTimeout(context.Background(), conformanceWait)
	defer cancel()
	require.NoError(t, c.awaitChildUp(upCtx, out.Harp))
	require.Eventually(t, func() bool { return len(c.LiveContainerRunIDs()) == 1 }, conformanceWait, 10*time.Millisecond)
	runID := c.LiveContainerRunIDs()[0]

	require.True(t, answers(t, c, addr), "the listener is open while the container run lives")
	require.Equal(t, []string{addr}, recordedListenAddrs(t, stateDir))

	c.terminateRun(runID, CauseStopped, "test")

	assert.False(t, answers(t, c, addr), "the listener closes when the last container run that needed it ends")
	assert.Empty(t, recordedListenAddrs(t, stateDir), "endpoint.json stops listing a closed listener")
}

// TestListener_RestartReopensOnlyForALiveContainerRun: a relaunched
// coordinator re-opens the recorded address while a container run it adopted
// is live (that runner redials it), and that run's end closes it again.
func TestListener_RestartReopensOnlyForALiveContainerRun(t *testing.T) {
	resetStrictness(t)
	teeHome(t)
	addr := lifetimeAddr(t)
	stateDir := t.TempDir()
	first := lifetimeCoordinator(t, stateDir, addr)
	out, err := first.AgentRun(context.Background(), ownerIdentity(), "worker", "task", "")
	require.NoError(t, err)
	// Up, not merely enqueued: a coordinator closed mid-launch fails the
	// launch, and the run would end rather than be adopted.
	upCtx, cancel := context.WithTimeout(context.Background(), conformanceWait)
	defer cancel()
	require.NoError(t, first.awaitChildUp(upCtx, out.Harp))
	require.Eventually(t, func() bool { return len(first.LiveContainerRunIDs()) == 1 }, conformanceWait, 10*time.Millisecond)
	runID := first.LiveContainerRunIDs()[0]
	require.Eventually(t, func() bool { return answers(t, first, addr) }, conformanceWait, 10*time.Millisecond)
	// A crash, not a Close: a clean Close ends the runs it holds, and the point
	// is a run that outlives its coordinator.
	crashCoordinator(first)
	require.Equal(t, []string{addr}, recordedListenAddrs(t, stateDir), "premise: the closed coordinator's file still records the address")

	second := lifetimeCoordinator(t, stateDir, addr)
	t.Cleanup(second.Close)
	require.Equal(t, []string{runID}, second.LiveContainerRunIDs(), "premise: the container run was adopted")
	assert.True(t, answers(t, second, addr), "the adopted container run's recorded address is re-opened")
	assert.Equal(t, []string{addr}, recordedListenAddrs(t, stateDir))

	second.terminateRun(runID, CauseStopped, "test")
	assert.False(t, answers(t, second, addr), "the adopted run's end closes the re-opened listener")
	assert.Empty(t, recordedListenAddrs(t, stateDir))
}
