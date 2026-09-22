//go:build linux

package hostpty

import (
	"context"
	"os/exec"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// TestStart_TheChildIsArmedToDieWithItsParent pins the mechanism the
// integration suite proves live (a hard-killed `ctxloom run` leaves no
// runner): the child on the pty carries PR_SET_PDEATHSIG(SIGTERM) beside
// its session leadership and controlling terminal.
func TestStart_TheChildIsArmedToDieWithItsParent(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.Command("sh", "-c", "exit 0")
	s, err := Start(ctx, cmd)
	require.NoError(t, err)
	defer s.Kill()
	require.NotNil(t, cmd.SysProcAttr)
	require.Equal(t, syscall.SIGTERM, cmd.SysProcAttr.Pdeathsig, "the child dies with a hard-killed originator")
	require.True(t, cmd.SysProcAttr.Setsid, "the child is its own session")
	require.True(t, cmd.SysProcAttr.Setctty, "the slave is its controlling terminal")
	_, err = s.Wait()
	require.NoError(t, err)
}
