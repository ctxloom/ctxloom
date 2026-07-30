package content

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/trust"
)

func openFixtureBundle(t *testing.T) (*TreeStore, Bundle) {
	t.Helper()
	store := fixtureStore(t)
	b, err := store.Open(context.Background(), "code-quality")
	require.NoError(t, err)
	return store, b
}

func TestFiles_EnumeratesEveryFileIncludingDotfiles(t *testing.T) {
	_, b := openFixtureBundle(t)
	files, err := b.Files(context.Background())
	require.NoError(t, err)

	assert.Contains(t, files, "fragments/solid.md")
	assert.Contains(t, files, "mcp/.postgres.meta.yaml", "a dot-prefixed sidecar must be enumerated")
	assert.Contains(t, files, "skills/code-reviewer/scripts/run.sh")
	assert.Contains(t, files, "profiles/strict.yaml")
	assert.True(t, isSorted(files), "Files must be deterministic: %v", files)
}

// Files is TOTAL: it must show the store's own machinery too, because an
// enumeration that hides .sigs would hide it from the very check that exists to
// notice added or removed files.
func TestFiles_IncludesSigsAndManifest(t *testing.T) {
	store, b := openFixtureBundle(t)
	writeFile(t, store.fsys, fixtureRoot+"/code-quality/"+ManifestPath, "# ctxloom-content-digest/1\n")
	writeFile(t, store.fsys, fixtureRoot+"/code-quality/.sigs/deadbeef.publish.v1.ctxloom.dev.aa.sig", "sig")

	files, err := b.Files(context.Background())
	require.NoError(t, err)
	assert.Contains(t, files, ManifestPath)
	assert.Contains(t, files, ".sigs/deadbeef.publish.v1.ctxloom.dev.aa.sig")
}

func TestBuildManifest_CoversEveryFileExceptItselfAndSigs(t *testing.T) {
	store, b := openFixtureBundle(t)
	writeFile(t, store.fsys, fixtureRoot+"/code-quality/.sigs/deadbeef.publish.v1.ctxloom.dev.aa.sig", "sig")

	m, err := BuildManifest(context.Background(), b)
	require.NoError(t, err)

	files, err := b.Files(context.Background())
	require.NoError(t, err)
	for _, f := range files {
		_, covered := m.Lookup(f)
		if ManifestCovers(f) {
			assert.True(t, covered, "%q must be covered by the manifest", f)
		} else {
			assert.False(t, covered, "%q must NOT be covered by the manifest", f)
		}
	}

	// The recorded hash must be the file's real hash.
	body, err := b.ReadFile(context.Background(), "fragments/solid.md")
	require.NoError(t, err)
	sum := sha256.Sum256(body)
	got, ok := m.Lookup("fragments/solid.md")
	require.True(t, ok)
	assert.Equal(t, hex.EncodeToString(sum[:]), got)
}

func TestManifest_RoundTripsThroughBytes(t *testing.T) {
	_, b := openFixtureBundle(t)
	m, err := BuildManifest(context.Background(), b)
	require.NoError(t, err)

	back, err := ParseManifest(m.Bytes())
	require.NoError(t, err)
	assert.Equal(t, m.Bytes(), back.Bytes())
	assert.Equal(t, m.Entries(), back.Entries())
}

func TestParseManifest_RejectsNonCanonicalAndMalformed(t *testing.T) {
	good := "# ctxloom-content-digest/1\n" +
		strings.Repeat("a", 64) + "  fragments/a.md\n" +
		strings.Repeat("b", 64) + "  fragments/b.md\n"

	cases := map[string]string{
		"no version marker": strings.Repeat("a", 64) + "  fragments/a.md\n",
		"unknown version":   "# ctxloom-content-digest/99\n" + strings.Repeat("a", 64) + "  x.md\n",
		"out of order": "# ctxloom-content-digest/1\n" +
			strings.Repeat("b", 64) + "  fragments/b.md\n" +
			strings.Repeat("a", 64) + "  fragments/a.md\n",
		"single space separator": "# ctxloom-content-digest/1\n" + strings.Repeat("a", 64) + " fragments/a.md\n",
		"short hash":             "# ctxloom-content-digest/1\n" + strings.Repeat("a", 63) + "  fragments/a.md\n",
		"uppercase hash":         "# ctxloom-content-digest/1\n" + strings.Repeat("A", 64) + "  fragments/a.md\n",
		"duplicate path": "# ctxloom-content-digest/1\n" +
			strings.Repeat("a", 64) + "  fragments/a.md\n" +
			strings.Repeat("b", 64) + "  fragments/a.md\n",
		"escaping path": "# ctxloom-content-digest/1\n" + strings.Repeat("a", 64) + "  ../escape.md\n",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseManifest([]byte(raw))
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrManifestFormat)
		})
	}

	m, err := ParseManifest([]byte(good))
	require.NoError(t, err)
	assert.Equal(t, 2, m.Len())
}

func TestBundleManifest_MissingIsItsOwnError(t *testing.T) {
	_, b := openFixtureBundle(t)
	_, err := b.Manifest(context.Background())
	assert.ErrorIs(t, err, ErrManifestMissing)
}

func TestVerifyContents_GreenOnAFreshlyBuiltManifest(t *testing.T) {
	store, b := openFixtureBundle(t)
	m, err := BuildManifest(context.Background(), b)
	require.NoError(t, err)
	require.NoError(t, store.PutManifest(context.Background(), b.ID(), m))

	loaded, err := b.Manifest(context.Background())
	require.NoError(t, err)
	assert.NoError(t, loaded.VerifyContents(context.Background(), b))
}

// HOSTILE PUBLISHER: an extra directory added to a signed tree. Nothing
// enumerates it as an item, so only the manifest's reverse direction can catch
// it.
func TestVerifyContents_FailsOnAnExtraDirectoryNoKindOwns(t *testing.T) {
	store, b := openFixtureBundle(t)
	m, err := BuildManifest(context.Background(), b)
	require.NoError(t, err)
	require.NoError(t, store.PutManifest(context.Background(), b.ID(), m))

	writeFile(t, store.fsys, fixtureRoot+"/code-quality/evil/payload.sh", "rm -rf /")

	err = m.VerifyContents(context.Background(), b)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrContentsMismatch)
	var ce *ContentsError
	require.True(t, errors.As(err, &ce))
	assert.Equal(t, []string{"evil/payload.sh"}, ce.Unclaimed)
	assert.Empty(t, ce.Missing)
	assert.Empty(t, ce.Mismatched)
}

func TestVerifyContents_FailsOnEditedBytes(t *testing.T) {
	store, b := openFixtureBundle(t)
	m, err := BuildManifest(context.Background(), b)
	require.NoError(t, err)
	require.NoError(t, store.PutManifest(context.Background(), b.ID(), m))

	writeFile(t, store.fsys, fixtureRoot+"/code-quality/fragments/solid.md", "---\ntags: []\n---\nsubstituted\n")

	err = m.VerifyContents(context.Background(), b)
	require.Error(t, err)
	var ce *ContentsError
	require.True(t, errors.As(err, &ce))
	assert.Equal(t, []string{"fragments/solid.md"}, ce.Mismatched)
}

func TestVerifyContents_FailsOnADeletedFile(t *testing.T) {
	store, b := openFixtureBundle(t)
	m, err := BuildManifest(context.Background(), b)
	require.NoError(t, err)
	require.NoError(t, store.PutManifest(context.Background(), b.ID(), m))

	require.NoError(t, store.fsys.Remove(fixtureRoot+"/code-quality/fragments/tricky.md"))

	err = m.VerifyContents(context.Background(), b)
	require.Error(t, err)
	var ce *ContentsError
	require.True(t, errors.As(err, &ce))
	assert.Equal(t, []string{"fragments/tricky.md"}, ce.Missing)
}

// F3(c): the manifest deliberately does not cover .sigs/ or itself, so writing a
// signature after building the manifest must NOT invalidate the tree.
func TestVerifyContents_AddingASignatureDoesNotBreakTheTree(t *testing.T) {
	store, b := openFixtureBundle(t)
	ctx := context.Background()
	m, err := BuildManifest(ctx, b)
	require.NoError(t, err)
	require.NoError(t, store.PutManifest(ctx, b.ID(), m))
	require.NoError(t, store.PutBundleSignature(ctx, b.ID(), "publish.v1.ctxloom.dev", []byte("armored-sig")))

	assert.NoError(t, m.VerifyContents(ctx, b))
}

func TestPutBundleSignature_RoundTrips(t *testing.T) {
	store, b := openFixtureBundle(t)
	ctx := context.Background()
	m, err := BuildManifest(ctx, b)
	require.NoError(t, err)
	require.NoError(t, store.PutManifest(ctx, b.ID(), m))
	require.NoError(t, store.PutBundleSignature(ctx, b.ID(), "publish.v1.ctxloom.dev", []byte("armored-sig")))

	sigs, err := b.BundleSignatures(ctx)
	require.NoError(t, err)
	require.Len(t, sigs, 1)
	assert.Equal(t, Namespace("publish.v1.ctxloom.dev"), sigs[0].Namespace)
	assert.Equal(t, []byte("armored-sig"), sigs[0].Bytes)
}

// The bundle signature is stored at a FIXED key, not a content-derived one, so
// rewriting the manifest leaves the signature reachable and it fails to verify —
// "tampered", not a silent downgrade to "unsigned".
func TestBundleSignature_SurvivesAManifestRewrite(t *testing.T) {
	store, b := openFixtureBundle(t)
	ctx := context.Background()
	m, err := BuildManifest(ctx, b)
	require.NoError(t, err)
	require.NoError(t, store.PutManifest(ctx, b.ID(), m))
	require.NoError(t, store.PutBundleSignature(ctx, b.ID(), "publish.v1.ctxloom.dev", []byte("armored-sig")))

	writeFile(t, store.fsys, fixtureRoot+"/code-quality/evil/payload.sh", "rm -rf /")
	m2, err := BuildManifest(ctx, b)
	require.NoError(t, err)
	require.NoError(t, store.PutManifest(ctx, b.ID(), m2))

	sigs, err := b.BundleSignatures(ctx)
	require.NoError(t, err)
	require.Len(t, sigs, 1, "the old signature must remain reachable so it can FAIL, not vanish")
}

// F2, enumeration half: a mis-extensioned hook is today silently dropped by
// walk(). It must fail loud instead — this is the withheld pre_tool guardrail
// reachable by typo.
func TestRefs_FailsLoudOnAMisExtensionedHook(t *testing.T) {
	store, b := openFixtureBundle(t)
	writeFile(t, store.fsys, fixtureRoot+"/code-quality/hooks/pre_tool/typo.yml", "event: pre_tool\n")

	_, err := b.Refs(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnclaimed)
	assert.Contains(t, err.Error(), "hooks/pre_tool/typo.yml")
}

func TestRefs_FailsLoudOnAnUnclaimedFileInAKindDirectory(t *testing.T) {
	store, b := openFixtureBundle(t)
	writeFile(t, store.fsys, fixtureRoot+"/code-quality/fragments/payload.txt", "smuggled")

	_, err := b.Refs(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnclaimed)
	assert.Contains(t, err.Error(), "fragments/payload.txt")
}

func TestRefs_StillGreenOnTheUntouchedFixture(t *testing.T) {
	_, b := openFixtureBundle(t)
	refs, err := b.Refs(context.Background())
	require.NoError(t, err)
	assert.NotEmpty(t, refs)
}

func TestReadFile_RefusesAPathOutsideTheBundle(t *testing.T) {
	_, b := openFixtureBundle(t)
	_, err := b.ReadFile(context.Background(), "../tooling/mcp/redis.yaml")
	assert.ErrorIs(t, err, ErrBadPath)
}

func TestPutManifest_RefusesAnEmptyManifest(t *testing.T) {
	store, b := openFixtureBundle(t)
	err := store.PutManifest(context.Background(), b.ID(), Manifest{})
	require.Error(t, err)
}

func TestBuildManifest_RefusesABundleWithNoCoveredFiles(t *testing.T) {
	fsys := afero.NewMemMapFs()
	require.NoError(t, fsys.MkdirAll(fixtureRoot+"/empty", 0o755))
	store, err := NewTreeStore(fsys, fixtureRoot, Provenance{IsLocal: true})
	require.NoError(t, err)
	b, err := store.Open(context.Background(), "empty")
	require.NoError(t, err)

	_, err = BuildManifest(context.Background(), b)
	require.Error(t, err)
}

var _ = trust.Ref{}

func isSorted(s []string) bool {
	for i := 1; i < len(s); i++ {
		if s[i-1] > s[i] {
			return false
		}
	}
	return true
}
