//go:build !windows

package cli

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	agentcoordpb "github.com/ctxloom/ctxloom/internal/adapters/coordgrpc/pb"
	"github.com/ctxloom/ctxloom/internal/adapters/hostpty"
	"github.com/ctxloom/ctxloom/internal/core/coord"
	"github.com/ctxloom/ctxloom/internal/testsupport/procalive"
)

// A process left holding the session's pty after the runner has gone — an
// MCP server or tool the engine spawned, which ignores the hangup it is sent
// when the engine or the runner exits — cannot hold the drive open. The engine
// runs on the very pty the frontend holds, so that holder sits on this pty;
// the drive gives the pty's tail its bound (ptyDrainGrace), releases the
// master, and reports the run's own outcome. The holder ignores SIGHUP, so
// the drain can never end on its own: the race the bound exists for is the
// only way the drive can return.
func TestDriveOwnedInteractive_AHolderLeftOnThePtyDoesNotHoldTheDriveOpen(t *testing.T) {
	noTerminal(t)
	pidFile := filepath.Join(t.TempDir(), "holder.pid")
	cmd := exec.Command("sh", "-c", `trap '' HUP; sleep 30 & echo $! > "$PIDFILE"; exit 0`)
	cmd.Env = append(os.Environ(), "PIDFILE="+pidFile)
	s, err := hostpty.Start(context.Background(), cmd)
	require.NoError(t, err)
	t.Cleanup(s.Kill)

	events := make(chan *agentcoordpb.AgentEvent, 1)
	events <- &agentcoordpb.AgentEvent{
		RunId: "run-1",
		Payload: &agentcoordpb.AgentEvent_RunCompleted{RunCompleted: &agentcoordpb.RunCompleted{
			Result: &agentcoordpb.Result{Status: agentcoordpb.Result_RUN_STATUS_SUCCEEDED},
		}},
	}
	st := &runState{ctx: context.Background(), pty: s, ownedRun: &ownedRunSession{outcome: &coord.RunOutcome{RunID: "run-1"}, events: events}}

	require.NoError(t, driveWithin(t, st, ptyDrainGrace+runnerExitAfterDrain+10*time.Second))

	raw, err := os.ReadFile(pidFile)
	require.NoError(t, err)
	pid, err := strconv.Atoi(strings.TrimSpace(string(raw)))
	require.NoError(t, err)
	t.Cleanup(func() { _ = syscall.Kill(pid, syscall.SIGKILL) })
	assert.True(t, procalive.Alive(pid), "the holder still holds the pty, and the drive returned all the same")
}
