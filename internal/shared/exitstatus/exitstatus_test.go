//go:build !windows

package exitstatus

import (
	"errors"
	"fmt"
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

// TestClassify: an exit error anywhere in the chain comes back carrying its
// classified status, its text and chain intact; an error with no process
// status, and nil, come back unchanged.
func TestClassify(t *testing.T) {
	runErr := exec.Command("sh", "-c", "kill -KILL $$").Run()
	wrapped := fmt.Errorf("host runner exited: %w", runErr)

	got := Classify(wrapped)
	var es interface{ ExitStatus() int }
	require.True(t, errors.As(got, &es))
	assert.Equal(t, 137, es.ExitStatus())
	assert.Equal(t, wrapped.Error(), got.Error(), "classifying must not change the text")
	var ee *exec.ExitError
	assert.True(t, errors.As(got, &ee), "the exit error stays reachable beneath")

	plain := errors.New("wait: no child processes")
	assert.Same(t, plain, Classify(plain))
	assert.NoError(t, Classify(nil))
}
