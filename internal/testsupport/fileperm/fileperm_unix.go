//go:build !windows

package fileperm

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

// Equal asserts got carries exactly want's permission bits.
func Equal(t testing.TB, want, got os.FileMode, msgAndArgs ...any) bool {
	t.Helper()
	return assert.Equal(t, want.Perm(), got.Perm(), msgAndArgs...)
}

// OwnerOnly asserts p grants nothing to its group or to others.
func OwnerOnly(t testing.TB, p string) bool {
	t.Helper()
	info, err := os.Stat(p)
	if !assert.NoError(t, err) {
		return false
	}
	return assert.Zero(t, info.Mode().Perm()&0o077, "%s has mode %04o; owner-only grants nothing to group or other", p, info.Mode().Perm())
}
