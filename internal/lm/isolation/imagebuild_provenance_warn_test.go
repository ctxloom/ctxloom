package isolation

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// TestComputeProvenanceDigest_UnresolvableSelfExeIsAnnounced pins that losing
// the image-staleness check is SAID rather than merely happening.
//
// An empty provenance digest turns imageRunsAsIs's staleness comparison off
// entirely (`wantProvenance != "" && ...`), and on a non-linux host that is not
// an edge case: selfLinuxExe rejects any GOOS other than linux, so
// resolveSelfExe fails on EVERY macOS/Windows run and the disable is the
// default. `ctxloom container provenance` prints the same empty digest and
// exits 0. Returning "" quietly is therefore the house silent-no-op shape --
// a check reporting success while doing nothing -- so the degrade must be
// announced.
func TestComputeProvenanceDigest_UnresolvableSelfExeIsAnnounced(t *testing.T) {
	orig := resolveSelfExe
	resolveSelfExe = func() (string, error) { return "", assert.AnError }
	t.Cleanup(func() { resolveSelfExe = orig })

	// The fixture must be hostile from computeProvenanceDigest's own vantage
	// point before anything else is asserted: a seam that silently still
	// resolves would make every assertion below vacuous.
	_, ferr := resolveSelfExe()
	require.Error(t, ferr, "the seam must actually fail, or this test proves nothing")

	var sink bytes.Buffer
	restore := clidiag.SetSink(&sink)
	t.Cleanup(restore)

	got := computeProvenanceDigest()
	require.Empty(t, got, "an unresolvable self-exe still yields no digest")

	out := sink.String()
	require.NotEmpty(t, out, "the disabled staleness check must be announced, not silently returned as an empty digest")
	assert.True(t, strings.Contains(out, "provenance"),
		"the warning must name what was disabled; got %q", out)
}

// TestComputeProvenanceDigest_ResolvableSelfExeIsQuiet is the other half: the
// announcement is a degrade signal, not chatter on the healthy path.
func TestComputeProvenanceDigest_ResolvableSelfExeIsQuiet(t *testing.T) {
	withFakeSelfExe(t)

	var sink bytes.Buffer
	restore := clidiag.SetSink(&sink)
	t.Cleanup(restore)

	require.NotEmpty(t, computeProvenanceDigest())
	assert.Empty(t, sink.String(), "a resolvable self-exe must produce no warning")
}
