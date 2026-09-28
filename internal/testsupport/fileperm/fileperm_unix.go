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
