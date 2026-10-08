//go:build !windows

package hostpty

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// startScript starts `sh -c script` on a pty with the given end grace, $DIR
// naming a fresh directory, and waits for the script to create $DIR/ready.
func startScript(t *testing.T, script string, grace time.Duration) (*session, string) {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = append(os.Environ(), "DIR="+dir)
	s, err := start(context.Background(), cmd, grace)
	require.NoError(t, err)
	t.Cleanup(s.Kill)
	// The master is read throughout, as the originator's drive reads it: on
	// macOS a child's exit cannot complete while its output sits unread, so
	// with no reader any byte the shell writes on its way out (a job notice)
	// would park the reap End waits on until the grace ran out.
	drain(s)
	require.Eventually(t, func() bool { _, err := os.Stat(filepath.Join(dir, "ready")); return err == nil },
		5*time.Second, 5*time.Millisecond)
	return s, dir
}

// End is how the coordinator stops a pty-hosted runner, so it must let the
// runner unwind through its teardown: SIGTERM first. The grace never runs
// out within the test, so End returning at all proves it returned on the
// child's exit, not by sitting the grace out — judged once End has
// returned, against what the child left behind, not against a clock.
func TestEnd_LetsTheChildRunItsTeardown(t *testing.T) {
	s, dir := startScript(t, `trap 'kill $!; : > "$DIR/torn-down"; exit 0' TERM; : > "$DIR/ready"; sleep 30 & wait`, testsupport.BudgetUntil(time.Time{}, false))

	testsupport.Within(t, testsupport.Budget(t), func() struct{} { s.End(); return struct{}{} },
		"End must return once a child that honours SIGTERM has exited")
	select {
	case <-s.Exited():
	default:
		t.Fatal("End returned before the child was gone")
	}
	require.FileExists(t, filepath.Join(dir, "torn-down"), "the child must have run its SIGTERM teardown, not been SIGKILLed")
}

// A child that ignores SIGTERM must not wedge End: it is SIGKILLed once the
// grace runs out.
func TestEnd_SIGKILLsAWedgedChildAfterTheGrace(t *testing.T) {
	s, _ := startScript(t, `trap '' TERM; : > "$DIR/ready"; exec sleep 30`, 300*time.Millisecond)

	testsupport.Within(t, 10*time.Second, func() struct{} { s.End(); return struct{}{} },
		"End must not wait on a wedged child past the grace")
	testsupport.Await(t, time.Second, s.Exited(), "a wedged child must be SIGKILLed once the grace runs out")
}
