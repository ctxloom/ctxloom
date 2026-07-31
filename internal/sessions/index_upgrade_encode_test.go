package sessions

import (
	"bytes"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// TestNormalizeTimestampNode_UnencodableValueIsAnnounced pins U099-F18's
// invariant: a failed re-encode must not be reported as "nothing to do".
//
// This is a test-seam row. Encoding a time.Time into a yaml.Node does not fail
// in practice, so the branch is only reachable through the encodeTimestampNode
// seam -- which means the honest red cannot be produced by reverting
// everything (the test would not compile). It is demonstrated instead with the
// seam PRESENT and only the warning removed.
//
// What the silence costs: normalizeTimestampNode returns false, Apply reports
// no change, the pipeline stages no upgrade, and the un-canonicalized value
// then fails yaml.Unmarshal -- so the operator sees "parse index" from
// loadLocked, which blames the file instead of the one value that could not be
// rewritten.
func TestNormalizeTimestampNode_UnencodableValueIsAnnounced(t *testing.T) {
	sentinel := errors.New("encode refused")
	orig := encodeTimestampNode
	encodeTimestampNode = func(*yaml.Node, time.Time) error { return sentinel }
	t.Cleanup(func() { encodeTimestampNode = orig })

	// The fixture must be hostile: the seam must actually be the failing one.
	require.ErrorIs(t, encodeTimestampNode(nil, time.Now()), sentinel)

	var sink bytes.Buffer
	restore := clidiag.SetSink(&sink)
	t.Cleanup(restore)

	n := &yaml.Node{Kind: yaml.ScalarNode, Value: "2026-05-28 16:57:20+00:00"}
	// And the input must be one the upgrade genuinely wants to rewrite,
	// otherwise the encode is never reached and the test proves nothing.
	_, ok := parseTimestamp(n.Value)
	require.True(t, ok, "the fixture value must parse, so normalization reaches the encode")

	changed := normalizeTimestampNode(n)

	require.False(t, changed, "the node truly did not change; false is the honest answer")
	require.Equal(t, "2026-05-28 16:57:20+00:00", n.Value, "and the value is untouched")
	require.Contains(t, sink.String(), "could not canonicalize",
		"the failure must be announced, not conflated with 'already canonical'; got %q", sink.String())
	require.Contains(t, sink.String(), "encode refused",
		"the underlying error must be carried, not dropped")
}

// TestNormalizeTimestampNode_AlreadyCanonicalIsSilent is the discriminator: the
// OTHER path that returns false must stay quiet, or the warning above says
// nothing about which case occurred -- which is the exact conflation the row
// names.
func TestNormalizeTimestampNode_AlreadyCanonicalIsSilent(t *testing.T) {
	var sink bytes.Buffer
	restore := clidiag.SetSink(&sink)
	t.Cleanup(restore)

	canonical := time.Now().UTC().Format(time.RFC3339Nano)
	n := &yaml.Node{Kind: yaml.ScalarNode, Value: canonical}

	require.False(t, normalizeTimestampNode(n), "an already-canonical node reports unchanged")
	require.Empty(t, sink.String(), "and says nothing: only a real failure warns")
}
