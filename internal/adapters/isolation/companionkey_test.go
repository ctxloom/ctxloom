package isolation

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	containerfiles "github.com/ctxloom/ctxloom/container"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// TestImageStale_UpdatingACompanionInvalidatesTheImage is the point of the
// change, asserted where it has to be true: through imageStale, the ONE gate
// that decides whether a present image is reused. A companion is baked INTO
// the image, so a new one must make the baked label read stale — before this,
// the label was ctxloom's version alone and an image holding an old ltk /
// taskloom / reprise was reused indefinitely.
func TestImageStale_UpdatingACompanionInvalidatesTheImage(t *testing.T) {
	withCompanions(t, map[string]string{"taskloom": "v1.0.0", "ltk": "v2.0.0"})
	baked := HostProvenanceDigest("")
	require.NotEmpty(t, baked, "the staleness gate must be live, or the assertions below prove nothing")
	labels := map[string]string{provenanceLabel: baked}

	require.False(t, imageStale(labels, HostProvenanceDigest("")),
		"nothing changed: the image must be REUSED, or this test only proves the key is unstable")

	withCompanions(t, map[string]string{"taskloom": "v1.0.1", "ltk": "v2.0.0"})
	assert.True(t, imageStale(labels, HostProvenanceDigest("")),
		"an updated companion must invalidate the image it is baked into")
}

// TestHostProvenanceDigest_CoversEveryCompanionAndCtxloomItself pins that each
// half of the key is load-bearing: every companion moves it, INSTALLING one
// moves it, and ctxloom's own version still moves it (the property
// happiest-snuggle added, which this change must not trade away).
func TestHostProvenanceDigest_CoversEveryCompanionAndCtxloomItself(t *testing.T) {
	base := map[string]string{"taskloom": "v1.0.0", "ltk": "v2.0.0", "reprise": "v3.0.0"}
	withCompanions(t, base)
	want := HostProvenanceDigest("")
	require.NotEmpty(t, want)

	// Every companion, one at a time: none may be a passenger in the digest.
	for name := range base {
		bumped := map[string]string{}
		for k, v := range base {
			bumped[k] = v
		}
		bumped[name] = base[name] + ".1"
		withCompanions(t, bumped)
		assert.NotEqual(t, want, HostProvenanceDigest(""),
			"a new %s must change the key; it is baked into the image", name)
	}

	// Absent -> installed is a different image, so it must be a different key.
	withCompanions(t, map[string]string{"taskloom": "v1.0.0"})
	alone := HostProvenanceDigest("")
	withCompanions(t, map[string]string{"taskloom": "v1.0.0", "ltk": "v2.0.0"})
	assert.NotEqual(t, alone, HostProvenanceDigest(""),
		"installing a companion changes what gets staged, so it must change the key")

	// ctxloom's own version, with the companions held still.
	withCompanions(t, base)
	orig := binaryVersion
	t.Cleanup(func() { SetBinaryVersion(orig) })
	SetBinaryVersion("v0.8.0-def5678-20260904T031516")
	assert.NotEqual(t, want, HostProvenanceDigest(""),
		"ctxloom's own version must still contribute")
}

// TestHostProvenanceDigest_LabelStaysReadable: the label is a docker LABEL
// value, the output of `ctxloom container provenance`, and the string
// imageStale compares. Adding the companion half must not make it something a
// label cannot carry or a reader cannot decompose — the ctxloom version stays
// the LEADING segment, and the base content hash stays the trailing one.
func TestHostProvenanceDigest_LabelStaysReadable(t *testing.T) {
	withCompanions(t, map[string]string{"taskloom": "v1.0.0"})
	got := HostProvenanceDigest("")
	require.NotEmpty(t, got)

	assert.True(t, strings.HasPrefix(got, versionProvenanceKey(testStamp)+companionKeySeparator),
		"the ctxloom version must remain the leading, readable segment of %q", got)
	assert.True(t, strings.HasSuffix(got, "-"+baseContentHash(containerfiles.Base())),
		"the base generation must remain the trailing segment of %q", got)
	for _, r := range got {
		assert.True(t, r == '.' || r == '-' || r == '+' ||
			(r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'),
			"provenance %q carries %q, which a label value / shell comparison should not have to quote", got, r)
	}
}

// TestCompanionVersionKey_UnreportableCompanionRefusesByDefault pins the
// ruling: a companion that is STAGED but cannot report a version blocks,
// loudly. Recording it as "absent" would rebuild the very hole this change
// closes — an image that reads fresh while carrying an unknown binary — so the
// finding must be FATAL by default.
func TestCompanionVersionKey_UnreportableCompanionRefusesByDefault(t *testing.T) {
	// "" means present on PATH, version probe fails.
	withCompanions(t, map[string]string{"taskloom": "v1.0.0", "ltk": ""})

	strictness.Reset()
	t.Cleanup(strictness.Reset)
	mark := strictness.Checkpoint()
	t.Cleanup(func() { strictness.Close(mark) })

	_ = HostProvenanceDigest("")

	err := strictness.Mode{}.FindingsError(mark)
	require.Error(t, err, "an unreportable staged companion must be a fatal finding, not a silent omission")
	assert.Contains(t, err.Error(), "ltk", "the refusal must name WHICH companion; got %q", err)
	assert.Contains(t, err.Error(), "fix:", "the refusal must carry a remedy; got %q", err)
}

// TestCompanionVersionKey_UnreportableCompanionWarnsUnderDegraded is the other
// arm, and it is not optional: --degraded is a promise that the user still
// reaches a working LLM. A degraded run must WARN and still produce a usable
// key.
func TestCompanionVersionKey_UnreportableCompanionWarnsUnderDegraded(t *testing.T) {
	withCompanions(t, map[string]string{"taskloom": "v1.0.0", "ltk": ""})

	strictness.Reset()
	t.Cleanup(strictness.Reset)
	mark := strictness.Checkpoint()
	t.Cleanup(func() { strictness.Close(mark) })

	got := HostProvenanceDigest("")

	assert.NoError(t, strictness.Mode{Degraded: true}.FindingsError(mark),
		"--degraded must warn and continue, never refuse over a companion probe")
	assert.NotEmpty(t, got, "a degraded run still needs a usable provenance key")
}

// TestCompanionVersionKey_UnreportableIsNotTreatedAsAbsent: the degraded key
// must still SAY the companion is there but unknown. Collapsing it onto the
// absent case would make "ltk installed but broken" and "ltk not installed"
// the same image identity.
func TestCompanionVersionKey_UnreportableIsNotTreatedAsAbsent(t *testing.T) {
	strictness.Reset()
	t.Cleanup(strictness.Reset)

	withCompanions(t, map[string]string{"taskloom": "v1.0.0"})
	absent := companionVersionKey()

	withCompanions(t, map[string]string{"taskloom": "v1.0.0", "ltk": ""})
	unreportable := companionVersionKey()

	assert.NotEqual(t, absent, unreportable,
		"a present-but-unreportable companion must not key the same as one that is not installed")
}
