//go:build !windows

package exitstatus

import (
	"errors"
	"os/exec"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestOf_ShellNumbering: an ordinary exit reports its own code; a child that
// died on a signal reports 128+signum, as a shell would.
func TestOf_ShellNumbering(t *testing.T) {
	for script, want := range map[string]int{
		"exit 3":        3,
		"kill -KILL $$": 137,
		"kill -TERM $$": 143,
		"exit 255":      255,
	} {
		err := exec.Command("sh", "-c", script).Run()
		var ee *exec.ExitError
		require.True(t, errors.As(err, &ee), "%s: %v", script, err)
		assert.Equal(t, want, Of(ee), script)
	}
}
