package cli

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/attach"
	"github.com/ctxloom/ctxloom/internal/adapters/hostpty"
	"github.com/ctxloom/ctxloom/internal/adapters/isolation"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// interactiveEnvironment stands in for the run's environment: only its
// Interactive is read by the pty starter.
type interactiveEnvironment struct {
	isolation.Environment
	in isolation.Interactive
}

func (e interactiveEnvironment) Interactive(context.Context, isolation.RunnerRequest) (isolation.Interactive, error) {
	return e.in, nil
}

// fakePTY is a Session that records the teardown it is handed. Only the
// methods the starter's wiring reaches are implemented.
type fakePTY struct {
	hostpty.Session
	exited        chan struct{}
	exitErr       error
	ended, killed int
}

func (f *fakePTY) End()                    { f.ended++ }
func (f *fakePTY) Kill()                   { f.killed++ }
func (f *fakePTY) ExitErr() error          { return f.exitErr }
func (f *fakePTY) Exited() <-chan struct{} { return f.exited }

// recordingStarter is a hostpty.Starter that hands back a fake session (or
// err) and records what it was asked to start, and under which ctx.
type recordingStarter struct {
	session *fakePTY
	err     error
	cmds    []*exec.Cmd
	ctxs    []context.Context
}

func (r *recordingStarter) start(ctx context.Context, cmd *exec.Cmd) (hostpty.Session, error) {
	r.cmds = append(r.cmds, cmd)
	r.ctxs = append(r.ctxs, ctx)
	if r.err != nil {
		return nil, r.err
	}
	return r.session, nil
}

func cancelledCtx() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}

// TestPTYStarter_HostRunnerStartsOnTheInjectedPort: a host runner is started
// through the Starter the run was given — on a ctx the launch's cancellation
// cannot reach (teardown has one door) — the session is recorded for the
// drive, and the coordinator is handed End (the master left to the drive's
// drain) and ExitErr (which never closes it).
func TestPTYStarter_HostRunnerStartsOnTheInjectedPort(t *testing.T) {
	cmd := exec.Command("runner")
	exitErr := errors.New("runner exited with status 3")
	rec := &recordingStarter{session: &fakePTY{exited: make(chan struct{}), exitErr: exitErr}}
	st := &runState{env: interactiveEnvironment{in: isolation.Interactive{Cmd: cmd}}}

	owned, err := st.ptyStarter(rec.start)(cancelledCtx(), nil)
	require.NoError(t, err)

	require.Len(t, rec.cmds, 1, "the runner is started exactly once, through the injected Starter")
	require.Same(t, cmd, rec.cmds[0])
	require.NoError(t, rec.ctxs[0].Err(), "the runner must not die with the launch ctx")
	require.Same(t, rec.session, st.pty, "the session is the drive's")
	require.Empty(t, owned.ContainerName)

	require.Same(t, exitErr, owned.Wait(), "the coordinator's Wait is the session's ExitErr")
	owned.Kill()
	require.Equal(t, 1, rec.session.ended, "the coordinator ends the runner")
	require.Zero(t, rec.session.killed, "the coordinator must not release the master the drive still reads")
}

// TestPTYStarter_ContainerRunnerStartsOnTheSamePortBeneathAttach: a container
// launch starts the runtime CLI on the SAME Starter, wrapped by attach, whose
// End removes the container by name; the roster gets that name.
func TestPTYStarter_ContainerRunnerStartsOnTheSamePortBeneathAttach(t *testing.T) {
	cmd := exec.Command("docker")
	torn := make(chan struct{})
	rec := &recordingStarter{session: &fakePTY{exited: make(chan struct{})}}
	st := &runState{env: interactiveEnvironment{in: isolation.Interactive{
		Cmd: cmd, Name: "ctr-7",
		Teardown: func(context.Context) error { close(torn); return nil },
	}}}

	owned, err := st.ptyStarter(rec.start)(cancelledCtx(), nil)
	require.NoError(t, err)

	require.Len(t, rec.cmds, 1)
	require.Same(t, cmd, rec.cmds[0], "the runtime CLI is started through the injected Starter")
	require.NoError(t, rec.ctxs[0].Err(), "the runtime CLI must not die with the launch ctx")
	require.Equal(t, "ctr-7", owned.ContainerName)
	as, ok := st.pty.(*attach.Session)
	require.True(t, ok, "a container's session is attach's, for teardown by name")
	require.Equal(t, "ctr-7", as.Name())

	close(rec.session.exited) // the CLI is gone: the removal is final, no grace kill
	owned.Kill()
	testsupport.Await(t, 5*time.Second, torn, "ending a container run removes the container")
}

// TestPTYStarter_StartFailureIsReportedAndNothingIsRecorded: a pty that
// cannot be started is the launch's error, and the drive is left nothing to
// pump.
func TestPTYStarter_StartFailureIsReportedAndNothingIsRecorded(t *testing.T) {
	boom := errors.New("no pty")
	for _, in := range []isolation.Interactive{
		{Cmd: exec.Command("runner")},
		{Cmd: exec.Command("docker"), Name: "ctr-8", Teardown: func(context.Context) error { return nil }},
	} {
		rec := &recordingStarter{err: boom}
		st := &runState{env: interactiveEnvironment{in: in}}
		_, err := st.ptyStarter(rec.start)(context.Background(), nil)
		require.ErrorIs(t, err, boom)
		require.Nil(t, st.pty)
	}
}
