//go:build !windows

package pidalive

import (
	"errors"
	"fmt"
	"os"
	"syscall"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestClassifySignalErr_UnwrapsTheErrno pins that the signal-0 outcome is
// classified by errors.Is rather than by ==. The bare-errno cases are what the
// runtime produces today; the WRAPPED cases are the ones == gets wrong, and
// getting them wrong is not a harmless miscategorisation — a wrapped ESRCH
// falling through to Unsure reads as "err toward alive", so a confidently dead
// owner keeps its lock or its worktree forever.
func TestClassifySignalErr_UnwrapsTheErrno(t *testing.T) {
	// Fixture check: a wrapped errno must genuinely defeat ==, or the wrapped
	// cases below would pass for either implementation and prove nothing.
	wrappedEPERM := fmt.Errorf("probing pid: %w", syscall.EPERM)
	require.NotEqual(t, error(syscall.EPERM), wrappedEPERM)
	require.True(t, errors.Is(wrappedEPERM, syscall.EPERM))

	cases := []struct {
		name string
		err  error
		want State
	}{
		{name: "no error", err: nil, want: Alive},
		{name: "bare EPERM", err: syscall.EPERM, want: Alive},
		{name: "wrapped EPERM", err: wrappedEPERM, want: Alive},
		{name: "bare ESRCH", err: syscall.ESRCH, want: Dead},
		{name: "wrapped ESRCH", err: fmt.Errorf("probing pid: %w", syscall.ESRCH), want: Dead},
		{name: "ErrProcessDone", err: os.ErrProcessDone, want: Dead},
		{name: "wrapped ErrProcessDone", err: fmt.Errorf("probing pid: %w", os.ErrProcessDone), want: Dead},
		{name: "an errno this probe cannot interpret", err: syscall.EINVAL, want: Unsure},
		{name: "not an errno at all", err: errors.New("something else"), want: Unsure},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, classifySignalErr(tc.err))
		})
	}
}

// EACCES must not be widened into EPERM. syscall.Errno.Is maps both onto
// fs.ErrPermission, and a careless "permission-ish means alive" would make an
// unrelated errno report a live process.
func TestClassifySignalErr_DoesNotWidenPermissionErrnos(t *testing.T) {
	assert.Equal(t, Unsure, classifySignalErr(syscall.EACCES))
}
