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
func startScript(t *testing.T, script string, grace time.Duration) (*Session, string) {
	t.Helper()
	dir := t.TempDir()
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = append(os.Environ(), "DIR="+dir)
	s, err := start(context.Background(), cmd, grace)
	require.NoError(t, err)
	t.Cleanup(s.Kill)
	require.Eventually(t, func() bool { _, err := os.Stat(filepath.Join(dir, "ready")); return err == nil },
		5*time.Second, 5*time.Millisecond)
	return s, dir
}

// End is how the coordinator stops a pty-hosted runner, so it must let the
// runner unwind through its teardown: SIGTERM first. The grace is far longer
// than the bound on End, so passing also proves End did not sit it out.
func TestEnd_LetsTheChildRunItsTeardown(t *testing.T) {
	s, dir := startScript(t, `trap 'kill $!; : > "$DIR/torn-down"; exit 0' TERM; : > "$DIR/ready"; sleep 30 & wait`, time.Minute)

	testsupport.Within(t, 10*time.Second, func() struct{} { s.End(); return struct{}{} },
		"End must return once a child that honours SIGTERM has exited")
	testsupport.Await(t, time.Second, s.Exited(), "End must not return before the child is gone")
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
