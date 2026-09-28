//go:build windows

package fileperm

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

// ownerWrite is the one permission bit Windows stores: the read-only
// attribute, which os reports as the absence of the write bits. Every other
// bit of a Windows FileMode is synthesized (0666/0777, or 0444/0555).
const ownerWrite os.FileMode = 0o200

// Equal asserts got is writable exactly when want is.
func Equal(t testing.TB, want, got os.FileMode, msgAndArgs ...any) bool {
	t.Helper()
	return assert.Equal(t, want&ownerWrite, got&ownerWrite, msgAndArgs...)
}
