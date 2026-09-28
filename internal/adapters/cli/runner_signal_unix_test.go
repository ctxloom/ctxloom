//go:build !windows

package cli

import (
	"context"
	"os"
	"os/signal"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// A pty-hosted runner is its session's leader, so when the originator dies
// and the pty's master is last closed the kernel SIGHUPs it. That hangup is a
// death signal and must end the runner through its teardown — including when
// the runner was started under nohup and inherited SIGHUP as ignored, which
// signal.Ignore stands in for here.
func TestRunnerContext_HangupEndsTheRunnerEvenWhenInheritedIgnored(t *testing.T) {
	signal.Ignore(syscall.SIGHUP)
	t.Cleanup(func() { signal.Reset(syscall.SIGHUP) })

	ctx, stop, err := runnerContext(context.Background())
	require.NoError(t, err)
	defer stop()

	require.NoError(t, syscall.Kill(os.Getpid(), syscall.SIGHUP))
	testsupport.Await(t, 5*time.Second, ctx.Done(), "a SIGHUP must end the runner's context")
}
