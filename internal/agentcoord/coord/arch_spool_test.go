//go:build arch

package coord

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestArch_SpoolWrite_HappensOnlyInTheCourier pins the invariant that gives the
// spool its one-write-one-ring guarantee: a message file appears on disk in
// exactly one place, and that place also rings the doorbell. If a second caller
// could reach the writer cache, it could create a spool file that nobody is
// ever told about — the file would sit there until the next 30s sweep, or
// forever if the recipient never sweeps.
func TestArch_SpoolWrite_HappensOnlyInTheCourier(t *testing.T) {
	got := referencingFiles(t, packageDir(t), "writerFor", false)
	assert.Equal(t, []string{"spoolcourier.go"}, got,
		"only the courier may reach a spool writer: it is what pairs the write with the ring. "+
			"If you added a caller, route it through spoolCourier.SendProjected instead; "+
			"if you MOVED the courier, update this test to name its new file.")
}

// TestArch_RingSpool_ReachedOnlyThroughTheCourier is the other half. ringSpool
// is fire-and-forget on both peers, so a ring raised beside a write (rather
// than by the courier that just wrote) is indistinguishable from a correct one
// until a message goes missing. The two files below do not CALL it: each
// installs it as a courier's ring field, which is the whole point.
func TestArch_RingSpool_ReachedOnlyThroughTheCourier(t *testing.T) {
	got := referencingFiles(t, packageDir(t), "ringSpool", false)
	assert.Equal(t, []string{"spooldelivery.go", "spoolturnresult.go"}, got,
		"ringSpool belongs to the courier: these two files may only hand it to one as its ring. "+
			"A new file here means someone rings without writing through the courier — "+
			"make it go through spoolCourier.SendProjected (or Announce for a ring with no write).")
}
