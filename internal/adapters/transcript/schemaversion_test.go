package transcript

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// futureVersionLine is one syntactically perfect canonical line carrying a
// schema version this build does not know.
func futureVersionLine() []byte {
	return fmt.Appendf(nil,
		`{"v":%d,"harp":"h","engine":"claude-code","seq":0,"ts":"2026-07-31T00:00:00Z","kind":"entry","entry":{"type":"user","content":"hi"}}`+"\n",
		SchemaVersion+7)
}

// TestParseTranscriptFile_VersionMismatchIsDiscriminable pins a seam the
// fail-loud version contract used to lack.
//
// "Refusing to guess at an unrecognized shape" is the right call, but every
// consumer downstream reduced it to `err != nil` and handled it exactly like a
// corrupt file — skipped from a listing, degraded to live-only, retried on the
// next poll tick forever. None of those is wrong on its own; what makes the
// contract toothless is that not one of them CAN tell "this file is damaged"
// from "this build is older than this file", which are opposite instructions
// to the user. A distinct error type is what makes the distinction reachable
// from several layers up.
func TestParseTranscriptFile_VersionMismatchIsDiscriminable(t *testing.T) {
	testsupport.Isolate(t)
	path := writeTempTranscript(t, "future-version-harp", futureVersionLine())

	_, err := ParseTranscriptFile(path, "future-version-harp")
	require.Error(t, err)

	var vErr *SchemaVersionError
	require.True(t, errors.As(err, &vErr),
		"a version refusal must be distinguishable from a corrupt file, not just non-nil")
	assert.Equal(t, SchemaVersion+7, vErr.Found)
	assert.Equal(t, SchemaVersion, vErr.Known)
	assert.Equal(t, path, vErr.Path)

	// A genuinely corrupt file must NOT classify as a version problem, or the
	// discrimination is worthless in the direction that matters.
	corrupt := writeTempTranscript(t, "corrupt-harp", []byte("not json\n"))
	_, cErr := ParseTranscriptFile(corrupt, "corrupt-harp")
	require.Error(t, cErr)
	assert.False(t, errors.As(cErr, &vErr), "an unparseable file is not a version mismatch")
}
