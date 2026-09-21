//go:build arch

package runner

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport/sourcedir"
)

// TestArch_RingSpool_ReachedOnlyThroughTheCourier is the runner side of the
// spool's one-write-one-ring guarantee: RingSpool is fire-and-forget, so a
// ring raised beside a write (rather than by the courier that just wrote) is
// indistinguishable from a correct one until a message goes missing. The file
// below does not CALL it: it installs it as the outbound courier's ring
// field, which is the whole point.
func TestArch_RingSpool_ReachedOnlyThroughTheCourier(t *testing.T) {
	dir, err := sourcedir.Dir()
	require.NoError(t, err)
	got := sourcedir.ReferencingFiles(t, dir, "RingSpool", false)
	assert.Equal(t, []string{"spoolturnresult.go"}, got,
		"RingSpool belongs to the courier: this file may only hand it to one as its ring. "+
			"A new file here means someone rings without writing through the courier — "+
			"make it go through coord.SpoolCourier.SendProjected (or Announce for a ring with no write).")
}
