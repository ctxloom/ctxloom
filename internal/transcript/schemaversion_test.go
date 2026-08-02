package transcript

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// futureVersionLine is one syntactically perfect canonical line carrying a
// schema version this build does not know.
func futureVersionLine() []byte {
	return fmt.Appendf(nil,
		`{"v":%d,"harp":"h","engine":"codex","seq":0,"ts":"2026-07-31T00:00:00Z","kind":"entry","entry":{"type":"user","content":"hi"}}`+"\n",
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
// to the user. A distinct error type is what makes the distinction reachable,
// the same way NoCanonicalTranscriptError already makes "never captured"
// reachable from several layers up.
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

// TestListSessions_VersionMismatchWarningIsActionable pins the payload of the
// diagnostic, not merely that one is emitted: a user whose ctxloom is too old
// to read a transcript must be told THAT, rather than being told the file is
// unreadable and left to conclude their session was lost.
func TestListSessions_VersionMismatchWarningIsActionable(t *testing.T) {
	testsupport.Isolate(t)
	const projectDir = "/proj/version-skew"

	good := "codex-fixture-harp"
	installFixture(t, "codex", good)
	future := "future-version-harp"
	writeTempTranscript(t, future, futureVersionLine())

	store := sessions.NewMemStore()
	mint(t, store, good, projectDir)
	time.Sleep(2 * time.Millisecond)
	mint(t, store, future, projectDir)

	var sink bytes.Buffer
	t.Cleanup(clidiag.SetSink(&sink))

	metas, err := NewCanonicalHistory(projectDir, store).ListSessions(context.Background())
	require.NoError(t, err)
	require.Len(t, metas, 1, "the readable session is still listed")

	out := sink.String()
	require.Contains(t, out, future, "the warning must name the harp it skipped")
	assert.Contains(t, out, "newer", "the warning must say the FILE is newer than this build")
	assert.Contains(t, out, "upgrade", "and say what the user can do about it")
}
