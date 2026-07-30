package coord

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/agentcoord"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// openFDPaths lists the paths this process currently holds open. Linux-only
// (procfs); the test that uses it skips elsewhere rather than asserting
// nothing.
//
// Every assertion over this census must name an EXACT path the calling test
// owns. The census is process-global and this package's tests all run in one
// binary, so a match on the transcript FILE NAME alone is a claim about every
// goroutine in the process: a straggler from an earlier test that records an
// event after its own HOME override was restored opens
// <this test's HOME>/.ctxloom/sessions/<its own harp>/transcript.jsonl, whose
// leaf is identical and whose lifetime no assertion here controls. That is a
// statement about the suite's scheduling, not about the subject under test.
func openFDPaths(t *testing.T) []string {
	t.Helper()
	const fdDir = "/proc/self/fd"
	entries, err := os.ReadDir(fdDir)
	if err != nil {
		t.Skipf("no procfs on this platform: %v", err)
	}
	var out []string
	for _, e := range entries {
		target, rerr := os.Readlink(filepath.Join(fdDir, e.Name()))
		if rerr != nil {
			continue // the fd closed under us; not our transcript either way
		}
		out = append(out, target)
	}
	return out
}

// TestEngineHost_CloseJoinsTranscriptCapture pins that EngineHost.Close is a
// real barrier for the transcript-capture chain, which is what the
// "two untracked goroutines" observation is actually about.
//
// transcript.TeeAndClose does dispatch two bare goroutines, in another
// package — but they are transitively joined by this file's own tracked
// group: adapt (goTracked) is their SOLE consumer and ranges the forwarded
// channel to close, and the forwarding goroutine closes that channel only
// after it has recorded every event and run rec.Close(). So Close's
// waitTracked join on adapt is also a join on both of them, and the
// discipline's actual invariant — no goroutine still touching eh/home state
// once Close returns — holds.
//
// The observable consequence, asserted here with NO Eventually: the moment
// Close returns, the run's transcript is complete AND its file descriptor is
// already released. An untracked capture chain would leave both pending.
func TestEngineHost_CloseJoinsTranscriptCapture(t *testing.T) {
	testsupport.Isolate(t)
	home := &fakeEngineHome{}
	sc := &scriptedChat{}
	eh := NewEngineHost(context.Background(), sc, "claude-code", "run-1")
	eh.BindHome(home)

	resp := eh.Handle(&agentcoordpb.RunnerRequest{Kind: &agentcoordpb.RunnerRequest_StartRun{StartRun: testStartRun("run-1")}})
	require.Equal(t, int32(0), resp.GetStatus().GetCode(), resp.GetStatus().GetMessage())
	require.Eventually(t, func() bool {
		for _, n := range home.customNames() {
			if n == CustomTurnIdle {
				return true
			}
		}
		return false
	}, 5*time.Second, 10*time.Millisecond, "the turn boundary must reach plane-1")

	transcriptPath, err := paths.HarpCanonicalTranscriptPath("child-harp-1")
	require.NoError(t, err)
	require.FileExists(t, transcriptPath)
	// The recorder is open right now — otherwise the release assertion below
	// would pass against a chain that never opened anything.
	require.Contains(t, openFDPaths(t), transcriptPath, "capture must hold the transcript open while the run is live")

	eh.Close()

	// No Eventually, no sleep: these hold the instant Close returns.
	//
	// ReportRunExited is adapt's LAST action, and adapt returns only once the
	// forwarding goroutine has closed its output — so this single assertion is
	// the structural link: Close joined adapt, therefore Close joined the
	// capture chain behind it.
	home.mu.Lock()
	exited := len(home.exited)
	home.mu.Unlock()
	assert.Equal(t, 1, exited, "Close must not return before adapt has run to completion")
	assert.NotContains(t, openFDPaths(t), transcriptPath,
		"Close must join the capture chain, so rec.Close() has already run")
	recs := readCanonicalTranscript(t, "child-harp-1")
	assert.Len(t, recs, 7, "every event the run produced is already recorded")

	// Close is idempotent: it neither disturbs what was captured nor reopens
	// the recorder it already joined.
	eh.Close()
	after := readCanonicalTranscript(t, "child-harp-1")
	assert.Len(t, after, 7)
	assert.NotContains(t, openFDPaths(t), transcriptPath,
		"a second Close must not reopen the transcript it already released")
}
