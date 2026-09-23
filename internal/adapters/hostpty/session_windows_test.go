package hostpty

import (
	"context"
	"os/exec"
	"testing"

	"github.com/creack/pty"
	"github.com/stretchr/testify/require"
)

// TestStart_RefusesOnWindows: there is no pty to start a child on, and the
// refusal is the typed sentinel callers can match, not a half-started child.
func TestStart_RefusesOnWindows(t *testing.T) {
	s, err := Start(context.Background(), exec.Command("cmd.exe", "/c", "exit"))
	require.ErrorIs(t, err, pty.ErrUnsupported)
	require.Nil(t, s)
}
