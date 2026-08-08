package vendorreader

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/transcript"
)

type namedAdapter struct{ name string }

func (namedAdapter) Convert(context.Context, transcript.Recorder, string) error { return nil }

// claudeLine mirrors the real claude-code declaration: the whole 2.x line,
// with .github/engine-versions.env's 2.1.214 pin inside it.
func claudeLine() []VersionedAdapter {
	return []VersionedAdapter{{
		Adapter:          namedAdapter{"claude-2x"},
		Versions:         VersionRange{MinInclusive: "2.0.0", MaxExclusive: "3.0.0"},
		ValidatedVersion: "2.1.214",
	}}
}

// The installed claude on this project's dev host (2.1.225) is AHEAD of the
// lock's pin (2.1.214). That must read fine: the pin is what was validated,
// not a ceiling on what a user may have.
func TestSelectAdapter_VersionAheadOfThePinButInsideTheRange(t *testing.T) {
	got, err := SelectAdapter("claude-code", "2.1.225", "harp", claudeLine())
	require.NoError(t, err)
	assert.Equal(t, namedAdapter{"claude-2x"}, got)
}

// And the installed codex (0.144.4) is BEHIND its pin (0.144.6). A user who
// has not upgraded yet must not be refused either — the range, not the pin, is
// what selection consults.
func TestSelectAdapter_VersionBehindThePinButInsideTheRange(t *testing.T) {
	codex := []VersionedAdapter{{
		Adapter:          namedAdapter{"codex-0.144"},
		Versions:         VersionRange{MinInclusive: "0.144.0", MaxExclusive: "0.145.0"},
		ValidatedVersion: "0.144.6",
	}}
	got, err := SelectAdapter("codex", "0.144.4", "harp", codex)
	require.NoError(t, err)
	assert.Equal(t, namedAdapter{"codex-0.144"}, got)
}

// THE SAFETY PROPERTY. A version outside every declared range must REFUSE, not
// pick the nearest adapter. Picking the nearest returns a transcript that
// looks fine and is not, on the path that feeds a model — the one failure this
// whole mechanism exists to prevent.
func TestSelectAdapter_OutOfRangeVersionRefusesInsteadOfPickingTheNearest(t *testing.T) {
	got, err := SelectAdapter("claude-code", "3.0.1", "sunny-harp", claudeLine())
	assert.Nil(t, got, "no adapter may be returned for a version nobody validated")

	var unsupported *UnsupportedVersionError
	require.ErrorAs(t, err, &unsupported)
	assert.Equal(t, "3.0.1", unsupported.Version)
	assert.Contains(t, err.Error(), "2.0.0",
		"the refusal must show what ctxloom DOES carry — that gap is the whole diagnosis and the bug report")
	assert.Contains(t, err.Error(), "refusing to read")
}

// The lower bound refuses too. A range is a claim in both directions; an old
// version silently handed to a new adapter is the same mis-parse as a new one
// handed to an old adapter.
func TestSelectAdapter_VersionBelowTheRangeRefuses(t *testing.T) {
	_, err := SelectAdapter("claude-code", "1.9.9", "", claudeLine())
	var unsupported *UnsupportedVersionError
	require.ErrorAs(t, err, &unsupported)
}

// Bounds are min-INCLUSIVE, max-EXCLUSIVE. Getting this wrong by one is
// exactly the sort of off-by-one that shows up as a mis-parse rather than an
// error, so it is pinned explicitly.
func TestSelectAdapter_BoundsAreMinInclusiveMaxExclusive(t *testing.T) {
	_, err := SelectAdapter("claude-code", "2.0.0", "", claudeLine())
	assert.NoError(t, err, "the lower bound is INCLUSIVE — the first validated version must be readable")

	_, err = SelectAdapter("claude-code", "3.0.0", "", claudeLine())
	assert.Error(t, err, "the upper bound is EXCLUSIVE — the version where the vendor may reshape the format is not covered")
}

// A session with NO recorded version — every session predating the field, and
// any whose engine could not be probed at start — must refuse, and must refuse
// with its OWN error, because the fix is different: for an unknown version
// someone writes an adapter, for an unrecorded one the session simply cannot
// be read back and the user needs to be told why rather than shown a
// fabricated transcript.
func TestSelectAdapter_NoRecordedVersionRefusesAndSaysWhy(t *testing.T) {
	got, err := SelectAdapter("claude-code", "", "sunny-harp", claudeLine())
	assert.Nil(t, got, "an unversioned session must not fall through to the newest adapter")

	var missing *NoRecordedVersionError
	require.ErrorAs(t, err, &missing)
	assert.Contains(t, err.Error(), "sunny-harp", "the refusal must name the session it is about")
	assert.Contains(t, err.Error(), "refusing to read rather than guessing")
}

// Whitespace is not a version. A blank-but-present value must take the SAME
// refusal path as an absent one, or a probe that recorded a stray newline
// would be treated as a real version and then fail somewhere less legible.
func TestSelectAdapter_BlankVersionIsTreatedAsUnrecorded(t *testing.T) {
	_, err := SelectAdapter("claude-code", "   ", "", claudeLine())
	var missing *NoRecordedVersionError
	assert.ErrorAs(t, err, &missing)
}

// A recorded value that is not a version at all (a vendor banner that slipped
// through, a corrupted index) refuses as unsupported and carries the parse
// failure — it is a different diagnosis from "your version is too new".
func TestSelectAdapter_UnparseableRecordedVersionRefuses(t *testing.T) {
	_, err := SelectAdapter("claude-code", "Claude Code build 42", "", claudeLine())
	var unsupported *UnsupportedVersionError
	require.ErrorAs(t, err, &unsupported)
	assert.ErrorContains(t, err, "not a usable version")
}

// An engine carrying no adapters at all refuses like any other unmatched
// version, and says so — "none at all" is a real answer a user can act on,
// where an empty range list rendered as nothing would read like a bug.
func TestSelectAdapter_NoCandidatesRefuses(t *testing.T) {
	_, err := SelectAdapter("kiro", "2.13.0", "", nil)
	var unsupported *UnsupportedVersionError
	require.ErrorAs(t, err, &unsupported)
	assert.Contains(t, err.Error(), "none at all")
}

// Selection is FIRST MATCH in declaration order, not nearest-or-newest.
// Overlapping ranges are a mistake in the declarations, and resolving them by
// some derived score would hide the mistake instead of leaving it visible.
func TestSelectAdapter_FirstMatchWinsInDeclarationOrder(t *testing.T) {
	overlapping := []VersionedAdapter{
		{Adapter: namedAdapter{"first"}, Versions: VersionRange{MinInclusive: "1.0.0", MaxExclusive: "3.0.0"}},
		{Adapter: namedAdapter{"second"}, Versions: VersionRange{MinInclusive: "2.0.0", MaxExclusive: "3.0.0"}},
	}
	got, err := SelectAdapter("x", "2.5.0", "", overlapping)
	require.NoError(t, err)
	assert.Equal(t, namedAdapter{"first"}, got)
}

// An unbounded range (no MaxExclusive) is allowed but means exactly what it
// says: everything at or above the floor.
func TestVersionRange_UnboundedAbove(t *testing.T) {
	r := VersionRange{MinInclusive: "2.0.0"}
	ok, err := r.Contains("99.0.0")
	require.NoError(t, err)
	assert.True(t, ok)

	ok, err = r.Contains("1.9.9")
	require.NoError(t, err)
	assert.False(t, ok)
}
