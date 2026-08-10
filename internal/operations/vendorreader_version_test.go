package operations

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/sessions"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/internal/transcript/vendorreader"
)

// stubEngineVersion is a codex version inside the real codex adapter's
// declared range, so a test that is not ABOUT version selection can hand
// ConvertVendorTranscript a session it will actually read.
const stubEngineVersion = "0.144.4"

// stubVersionedAdapter wraps a test adapter in the same declaration shape the
// real readers use, covering every version — a test that swaps the adapter is
// exercising conversion, not selection.
func stubVersionedAdapter(a vendorreader.VendorAdapter) []vendorreader.VersionedAdapter {
	return []vendorreader.VersionedAdapter{{
		Adapter:  a,
		Versions: vendorreader.VersionRange{MinInclusive: "0.0.1"},
	}}
}

// enginePins reads .github/engine-versions.env — the same real repository file
// internal/enginepins gates, not a fixture. The point is that the declared
// ranges and the CI lock are held against each other, so a range cannot
// quietly widen past what anyone has actually run.
//
// Located from this SOURCE FILE's own path rather than the working directory:
// other tests in this package chdir, and a relative read here would resolve
// against whichever one ran last.
func enginePins(t *testing.T) map[string]string {
	t.Helper()
	_, self, _, ok := runtime.Caller(0)
	require.True(t, ok)
	repoRoot := filepath.Dir(filepath.Dir(filepath.Dir(self)))
	raw, err := os.ReadFile(filepath.Join(repoRoot, ".github", "engine-versions.env"))
	require.NoError(t, err)
	out := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if ok {
			out[k] = v
		}
	}
	return out
}

// A declared version range is a CLAIM that those versions were validated. What
// makes it evidence rather than hope is .github/engine-versions.env, the
// tested-version lock CI keeps honest — so every adapter's range must contain
// its engine's pin, and its cited ValidatedVersion must BE that pin. Without
// this the ranges are just numbers somebody typed, and the refusal they
// produce means nothing.
func TestVendorReaderRanges_ContainThePinnedTestedVersion(t *testing.T) {
	pins := enginePins(t)
	byEngine := map[string]string{
		"claude-code": "CLAUDE_CODE_CLI_VERSION",
		"codex":       "CODEX_CLI_VERSION",
		"kiro":        "KIRO_CLI_VERSION",
	}

	for engine, key := range byEngine {
		pin, ok := pins[key]
		require.True(t, ok, "%s must be pinned in .github/engine-versions.env", key)

		reg, ok := vendorReaderRegistry[engine]
		require.True(t, ok, "%s must have a vendor reader", engine)
		require.NotEmpty(t, reg.adapters, "%s must declare at least one versioned adapter", engine)

		adapter, err := vendorreader.SelectAdapter(engine, pin, "", reg.adapters)
		require.NoError(t, err, "%s's pinned tested version %s must be covered by some declared range", engine, pin)
		assert.NotNil(t, adapter)

		var cited []string
		for _, a := range reg.adapters {
			cited = append(cited, a.ValidatedVersion)
		}
		assert.Contains(t, cited, pin,
			"%s's adapters must CITE the pin as their validated version — a range whose citation has drifted from the lock is asserting a validation nobody performed", engine)
	}
}

// The refusal has to reach the READ path, not just exist in the selection
// helper. A session with no recorded engine version — every session predating
// the field — must return an error and write NOTHING, rather than being read
// by whichever adapter happens to be first.
func TestConvertVendorTranscript_UnrecordedVersionRefusesAndWritesNothing(t *testing.T) {
	harp := "convert-unversioned-harp"
	e := sessions.Entry{HarpName: harp, Backend: "codex", TranscriptPath: codexFixturePath}

	converted, err := ConvertVendorTranscript(context.Background(), e)
	assert.False(t, converted, "nothing may be attempted for a session whose format is unknown")
	require.Error(t, err)

	var missing *vendorreader.NoRecordedVersionError
	assert.ErrorAs(t, err, &missing)
	assert.Contains(t, err.Error(), harp, "the refusal must name the session, so a user can act on it")
	assert.False(t, hasCanonicalTranscript(harp),
		"a refused read must leave no canonical transcript — a half-written one is the plausible-but-wrong output this refuses to produce")
}

// A version ctxloom carries no adapter for refuses the same way, naming the
// version and the ranges it does carry.
func TestConvertVendorTranscript_UnknownVersionRefuses(t *testing.T) {
	harp := "convert-future-version-harp"
	e := sessions.Entry{HarpName: harp, Backend: "codex", TranscriptPath: codexFixturePath, EngineVersion: "9.9.9"}

	converted, err := ConvertVendorTranscript(context.Background(), e)
	assert.False(t, converted)
	require.Error(t, err)

	var unsupported *vendorreader.UnsupportedVersionError
	require.ErrorAs(t, err, &unsupported)
	assert.Equal(t, "9.9.9", unsupported.Version)
	assert.False(t, hasCanonicalTranscript(harp))
}

// A session whose vendor transcript cannot be located at all stays the QUIET
// nothing-to-do it has always been, even though it has no recorded version.
// Selection deliberately happens AFTER locate: a refusal is an actionable
// signal about a transcript that exists, and raising it for every unbound or
// long-vanished session would fire it constantly until nobody reads it.
func TestConvertVendorTranscript_UnlocatableSessionStaysSilentDespiteNoVersion(t *testing.T) {
	e := sessions.Entry{HarpName: "convert-unbound-harp", Backend: "codex"}

	converted, err := ConvertVendorTranscript(context.Background(), e)
	assert.False(t, converted)
	assert.NoError(t, err,
		"a session with nothing to convert must not shout about an unknown version — the refusal is for transcripts that actually exist")
}

// THE OTHER FAILURE LEVEL, which must NOT be collapsed into the one above.
// Once a validated adapter has been selected, a malformed LINE is skipped, not
// fatal: an adapter that aborted on the first bad byte would turn one corrupt
// line into an entirely lost session, which is the failure vendorreader exists
// to avoid (VendorAdapter's own contract). Refuse BEFORE selection, degrade
// AFTER it — collapse the two and you either lose sessions to one bad line or
// silently half-read an unvalidated format.
func TestConvertVendorTranscript_MalformedLineInAKnownVersionDegradesToPartial(t *testing.T) {
	testsupport.Isolate(t)
	harp := "convert-partial-known-version-harp"

	good, err := os.ReadFile(codexFixturePath)
	require.NoError(t, err)
	corrupted := filepath.Join(t.TempDir(), "corrupted-rollout.jsonl")
	require.NoError(t, os.WriteFile(corrupted,
		append([]byte("{not json at all\n"), good...), 0o644))

	e := sessions.Entry{
		HarpName:       harp,
		Backend:        "codex",
		TranscriptPath: corrupted,
		EngineVersion:  stubEngineVersion, // a version the codex adapter IS validated for
	}

	converted, err := ConvertVendorTranscript(context.Background(), e)
	require.NoError(t, err,
		"a bad LINE inside a known format is not a structural failure — only an unreadable source, a cancelled context or a failing recorder is")
	assert.True(t, converted)
	assert.NotEmpty(t, canonicalLines(t, harp),
		"the readable remainder of the transcript must still land: one corrupt line may not cost the whole session")
}
