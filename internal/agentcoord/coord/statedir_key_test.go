package coord

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestPathDerivedProjectKey_IsStable pins the on-disk name of a project's
// coordinator state dir for the path-derived fallback (production takes it
// whenever the project identity cannot be resolved). This value NAMES A
// DIRECTORY holding live journals: changing it silently orphans every existing
// project's state, so the golden exists to make any change deliberate — in
// particular, it must not move as a side effect of hardening the unrelated
// credential hash.
func TestPathDerivedProjectKey_IsStable(t *testing.T) {
	assert.Equal(t, "proj-b41edca44529", pathDerivedProjectKey("/home/somebody/work/proj"))
	assert.Equal(t, "proj-c2b04d89fa54", pathDerivedProjectKey("/srv/other/proj"),
		"the digest disambiguates two projects whose base name collides")
	assert.Equal(t, sanitizeKey(pathDerivedProjectKey("/home/somebody/work/proj")), pathDerivedProjectKey("/home/somebody/work/proj"),
		"the derived key must already be a single safe path segment")
}
