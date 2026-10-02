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
	s, err := start(ctx, exec.Command("sh", "-c", "exit 0"), endGrace)
	require.NoError(t, err)
	defer s.Kill()
	// The attributes the child was STARTED with: the command go-pty ran,
	// not the caller's, which is only ever a template.
	attr := s.cmd.SysProcAttr
	require.NotNil(t, attr)
	require.Equal(t, syscall.SIGTERM, attr.Pdeathsig, "the child dies with a hard-killed originator")
	require.True(t, attr.Setsid, "the child is its own session")
	require.True(t, attr.Setctty, "the slave is its controlling terminal")
	_, err = s.Wait()
	require.NoError(t, err)
}
