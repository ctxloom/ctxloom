package isolation

import (
	"context"
	"fmt"
	"os/exec"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// startedRunner builds a containerRunner around a real process running script,
// already started. exitWait is short because every test here either reaps
// promptly or is deliberately measuring the expiry.
func startedRunner(t *testing.T, name, script string, exitWait time.Duration) *containerRunner {
	t.Helper()
	cmd := exec.Command("sh", "-c", script)
	r := &containerRunner{
		runtime:  Docker{},
		name:     name,
		cmd:      cmd,
		waited:   make(chan struct{}),
		exitWait: exitWait,
	}
	require.NoError(t, cmd.Start())
	t.Cleanup(func() {
		if cmd.Process != nil {
			_ = cmd.Process.Kill()
		}
	})
	return r
}

func diagnoseLead(r *containerRunner) string {
	return fmt.Sprintf("plugin container %q (%s)", r.name, r.runtime.Name())
}

// TestDiagnose_NamesTheConfigRefusalRatherThanTheTransport is the defect this
// change exists for. A {worktree, container} run whose in-container ctxloom
// refuses over a fatal config finding exits 3 before the handshake; go-plugin
// then asks Diagnose what happened, and the answer used to be a fixed string
// about images, executables and socket mounts — every word of it about a
// transport fault that did not occur. A reader acting on it searches the one
// area guaranteed to be healthy.
//
// The assertion is on WHICH answer the exit status selects, not on wording.
func TestDiagnose_NamesTheConfigRefusalRatherThanTheTransport(t *testing.T) {
	ctx := context.Background()
	r := startedRunner(t, "ctxloom-iso-refused",
		fmt.Sprintf("exit %d", strictness.ExitCodeFatalFindings), time.Second)

	require.Error(t, r.Wait(ctx), "premise: the container exited non-zero")

	msg := r.Diagnose(ctx)

	assert.Equal(t, diagnoseLead(r)+fmt.Sprintf(diagnoseConfigRefusal, strictness.ExitCodeFatalFindings), msg)
	assert.NotContains(t, msg, diagnoseNoHandshake,
		"reporting a configuration refusal as a failed handshake is the defect itself")
}

// TestDiagnose_OtherNonZeroExitStillIsNotATransportFault keeps the middle case
// honest. Exit 3 is ctxloom's own startup-abort status, but ANY non-zero exit
// means the process died rather than failing to negotiate — so the answer must
// still point at the container's stderr first.
//
// Without this, collapsing the two exit branches into one (or widening the
// exit-3 branch to every non-zero code) would go unnoticed, and an ordinary
// crash would be reported as a config refusal with a --degraded suggestion that
// cannot help.
func TestDiagnose_OtherNonZeroExitStillIsNotATransportFault(t *testing.T) {
	ctx := context.Background()
	const code = 42
	require.NotEqual(t, strictness.ExitCodeFatalFindings, code, "premise: a status with no meaning of ours")

	r := startedRunner(t, "ctxloom-iso-crashed", fmt.Sprintf("exit %d", code), time.Second)
	require.Error(t, r.Wait(ctx))

	msg := r.Diagnose(ctx)

	assert.Equal(t, diagnoseLead(r)+fmt.Sprintf(diagnoseDiedBeforeHandshake, code), msg)
	assert.NotContains(t, msg, "--degraded",
		"--degraded overrides config findings; offering it for an arbitrary crash sends the reader somewhere it cannot help")
}

// TestDiagnose_WaitsForTheReapRatherThanRacingIt is the ORDERING pin, and the
// reason this fix is not a one-line read of cmd.ProcessState.
//
// go-plugin closes its stdout line channel BEFORE releasing the goroutine that
// calls Wait, and it invokes Diagnose from the receive on that closed channel.
// So Diagnose reliably runs BEFORE the process is reaped. An implementation
// that reads whatever status happens to be present would therefore report "no
// status" precisely in the case a status is about to exist — reverting to the
// transport wording for exactly the defect being fixed.
//
// The delayed Wait here reproduces that ordering deterministically rather than
// hoping the scheduler produces it.
func TestDiagnose_WaitsForTheReapRatherThanRacingIt(t *testing.T) {
	ctx := context.Background()
	r := startedRunner(t, "ctxloom-iso-late-reap",
		fmt.Sprintf("exit %d", strictness.ExitCodeFatalFindings), 5*time.Second)

	reaped := make(chan struct{})
	go func() {
		defer close(reaped)
		time.Sleep(200 * time.Millisecond)
		_ = r.Wait(ctx)
	}()

	// Called while the reap is still pending, as go-plugin calls it.
	msg := r.Diagnose(ctx)
	<-reaped

	assert.Equal(t, diagnoseLead(r)+fmt.Sprintf(diagnoseConfigRefusal, strictness.ExitCodeFatalFindings), msg,
		"Diagnose must wait for the status it is about to be given; racing it reports a transport fault for a config refusal")
}

// TestDiagnose_LiveContainerFallsBackToTheHandshakeWording pins the bound, and
// with it the honesty of the fallback. A handshake that failed while the
// container is STILL RUNNING (a garbage line, a truncated one) will never be
// reaped by anyone, so waiting must expire — and when it does, a transport
// fault genuinely is the live hypothesis, so the original wording is correct
// there and must not be replaced with config speculation.
//
// An unbounded wait would hang go-plugin's startup instead of failing it.
func TestDiagnose_LiveContainerFallsBackToTheHandshakeWording(t *testing.T) {
	ctx := context.Background()
	r := startedRunner(t, "ctxloom-iso-hung", "sleep 30", 50*time.Millisecond)

	start := time.Now()
	msg := r.Diagnose(ctx)
	elapsed := time.Since(start)

	assert.Equal(t, diagnoseLead(r)+diagnoseNoHandshake, msg,
		"nothing exited, so there is no status to report and the transport hypothesis stands")
	assert.GreaterOrEqual(t, elapsed, 50*time.Millisecond, "the bound must actually be waited out, not skipped")
	assert.Less(t, elapsed, 25*time.Second, "and it must expire rather than block startup on a container nobody will reap")
}

// TestDiagnose_ZeroExitReportsTheHandshakeNotAnExit covers the plugin that
// exited CLEANLY without ever negotiating. There is no failure of ours to name,
// so "exited 0 before the handshake" would be a distraction — the handshake
// really is what did not happen.
func TestDiagnose_ZeroExitReportsTheHandshakeNotAnExit(t *testing.T) {
	ctx := context.Background()
	r := startedRunner(t, "ctxloom-iso-clean", "exit 0", time.Second)
	require.NoError(t, r.Wait(ctx), "premise: a clean exit")

	assert.Equal(t, diagnoseLead(r)+diagnoseNoHandshake, r.Diagnose(ctx))
}

// TestWait_PublishesTheStatusOnceAndDoesNotBlockLaterReaders pins the two
// properties the publication needs beyond correctness of the value: it is
// readable repeatedly (Diagnose is not the only possible caller, and a
// one-shot channel receive would starve the second one), and it is cheap once
// published — no caller pays the bound after the reap.
func TestWait_PublishesTheStatusOnceAndDoesNotBlockLaterReaders(t *testing.T) {
	ctx := context.Background()
	r := startedRunner(t, "ctxloom-iso-published",
		fmt.Sprintf("exit %d", strictness.ExitCodeFatalFindings), time.Hour)
	require.Error(t, r.Wait(ctx))

	start := time.Now()
	for i := range 3 {
		code, ok := r.exitStatus()
		require.True(t, ok, "read %d: a published status must stay readable", i)
		assert.Equal(t, strictness.ExitCodeFatalFindings, code)
	}
	assert.Less(t, time.Since(start), time.Second,
		"an already-published status must not pay the wait bound — this would be an hour if it did")
}

// TestExitStatus_BareLiteralReportsNoStatus keeps the package's existing
// containerRunner literals (which construct no channel) working, and pins that
// the answer is "no status" rather than a bare -1 that would be reported as a
// real exit code.
func TestExitStatus_BareLiteralReportsNoStatus(t *testing.T) {
	r := &containerRunner{runtime: Docker{}, name: "ctxloom-iso-bare", cmd: exec.Command("true")}

	code, ok := r.exitStatus()

	assert.False(t, ok, "a runner that was never started has no status to report")
	assert.Zero(t, code)
	assert.Equal(t, diagnoseLead(r)+diagnoseNoHandshake, r.Diagnose(context.Background()))
}
