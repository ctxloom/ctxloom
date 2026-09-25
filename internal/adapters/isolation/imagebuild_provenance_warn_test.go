package isolation

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// TestHostVersionKey_UnstampedBinaryIsAnnounced pins that losing the
// image-staleness check is SAID rather than merely happening.
//
// An empty provenance key turns imageRunsAsIs's staleness comparison off
// entirely (`wantProvenance != "" && ...`), and `ctxloom container provenance`
// prints the same empty value and exits 0. Returning "" quietly is therefore
// the house silent-no-op shape — a check reporting success while doing
// nothing — so the degrade must be announced.
func TestHostImageKeys_UnstampedBinaryIsAnnounced(t *testing.T) {
	unsetVersionStamp(t)

	// The fixture must be hostile from hostImageKeys' own vantage point
	// before anything else is asserted: a stamp that still parsed would make
	// every assertion below vacuous.
	require.Empty(t, versionProvenanceKey(binaryVersion),
		"the stamp must actually be unusable, or this test proves nothing")

	var sink bytes.Buffer
	restore := clidiag.SetSink(&sink)
	t.Cleanup(restore)

	// warnProvenanceDisabled is WarnOnce, whose dedup is process-wide: without
	// this reset an identical warning fired by an earlier test in this binary
	// silently blanks the sink, and the assertion below fails for a reason that
	// has nothing to do with the behaviour it names.
	clidiag.ResetWarnOnce()
	t.Cleanup(clidiag.ResetWarnOnce)

	keys := hostImageKeys()
	tagKey, provenanceKey := keys.tag, keys.provenance
	require.Empty(t, tagKey, "an unstamped binary still yields no tag key")
	require.Empty(t, provenanceKey, "an unstamped binary still yields no provenance key")

	out := sink.String()
	require.NotEmpty(t, out, "the disabled staleness check must be announced, not silently returned as an empty key")
	assert.True(t, strings.Contains(out, "staleness"),
		"the warning must name what was disabled; got %q", out)
}

// TestHostImageKeys_StampedBinaryIsQuiet is the other half: the announcement
// is a degrade signal, not chatter on the healthy path.
func TestHostImageKeys_StampedBinaryIsQuiet(t *testing.T) {
	var sink bytes.Buffer
	restore := clidiag.SetSink(&sink)
	t.Cleanup(restore)
	clidiag.ResetWarnOnce()
	t.Cleanup(clidiag.ResetWarnOnce)

	keys := hostImageKeys()
	tagKey, provenanceKey := keys.tag, keys.provenance
	require.NotEmpty(t, tagKey, "TestMain's stamp must resolve a tag key")
	require.NotEmpty(t, provenanceKey, "TestMain's stamp must resolve a provenance key")
	assert.Empty(t, sink.String(), "a usable stamp must produce no warning")
}
