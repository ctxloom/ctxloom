package content

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Masterminds/semver/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/release"
)

const testHeader = "# ctxloom-bundle-manifest/1\n# name: b\n# version: 1.0.0\n"

func testVersion(t *testing.T, s string) *semver.Version {
	t.Helper()
	v, err := semver.StrictNewVersion(s)
	require.NoError(t, err)
	return v
}

func testRelease(t *testing.T, name, version string) release.Release {
	t.Helper()
	return release.Release{Name: name, Version: testVersion(t, version)}
}

func fixtureRelease(t *testing.T) release.Release {
	t.Helper()
	return testRelease(t, "code-quality", "1.0.0")
}

func oneEntry() []ManifestEntry {
	return []ManifestEntry{{Path: "fragments/a.md", SHA256: strings.Repeat("a", 64)}}
}

func TestManifest_RendersTheSignedReleaseHeader(t *testing.T) {
	rel := release.Release{
		Name:    "b",
		Version: testVersion(t, "1.3.0"),
		// Deliberately out of order: the manifest sorts them.
		Retracts: []release.Retraction{
			{Version: testVersion(t, "1.2.0"), Reason: "broken hook"},
			{Version: testVersion(t, "1.1.0"), Reason: "leaked token"},
		},
		Withdrawn: "superseded by c",
	}
	m, err := NewManifest(rel, oneEntry())
	require.NoError(t, err)
	want := "# ctxloom-bundle-manifest/1\n" +
		"# name: b\n" +
		"# version: 1.3.0\n" +
		"# retracts: 1.1.0 leaked token\n" +
		"# retracts: 1.2.0 broken hook\n" +
		"# withdrawn: superseded by c\n" +
		strings.Repeat("a", 64) + "  fragments/a.md\n"
	assert.Equal(t, want, string(m.Bytes()))

	back, err := ParseManifest(m.Bytes())
	require.NoError(t, err)
	got := back.Release()
	assert.Equal(t, "b", got.Name)
	assert.Equal(t, "1.3.0", got.Version.String())
	require.Len(t, got.Retracts, 2)
	assert.Equal(t, "1.1.0", got.Retracts[0].Version.String())
	assert.Equal(t, "leaked token", got.Retracts[0].Reason)
	assert.Equal(t, "superseded by c", got.Withdrawn)
	assert.Equal(t, m.Bytes(), back.Bytes())
}

func TestNewManifest_RefusesAnUnrenderableRelease(t *testing.T) {
	cases := map[string]release.Release{
		"no name":               {Version: testVersion(t, "1.0.0")},
		"nested name":           {Name: "a/b", Version: testVersion(t, "1.0.0")},
		"name with newline":     {Name: "a\n# version: 9.9.9", Version: testVersion(t, "1.0.0")},
		"no version":            {Name: "b"},
		"multi-line reason":     {Name: "b", Version: testVersion(t, "2.0.0"), Retracts: []release.Retraction{{Version: testVersion(t, "1.0.0"), Reason: "a\nb"}}},
		"empty reason":          {Name: "b", Version: testVersion(t, "2.0.0"), Retracts: []release.Retraction{{Version: testVersion(t, "1.0.0")}}},
		"nil retracted version": {Name: "b", Version: testVersion(t, "2.0.0"), Retracts: []release.Retraction{{Reason: "x"}}},
		"duplicate retraction": {Name: "b", Version: testVersion(t, "2.0.0"), Retracts: []release.Retraction{
			{Version: testVersion(t, "1.0.0"), Reason: "x"}, {Version: testVersion(t, "1.0.0"), Reason: "y"}}},
		"multi-line withdrawal": {Name: "b", Version: testVersion(t, "1.0.0"), Withdrawn: "a\rb"},
		"padded withdrawal":     {Name: "b", Version: testVersion(t, "1.0.0"), Withdrawn: " x"},
	}
	for name, rel := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := NewManifest(rel, oneEntry())
			assert.ErrorIs(t, err, ErrManifestFormat)
		})
	}
}

func TestParseManifest_HeaderGrammarIsStrict(t *testing.T) {
	line := strings.Repeat("a", 64) + "  fragments/a.md\n"
	cases := map[string]string{
		"old digest marker":       DigestVersionMarker + "\n" + line,
		"no name":                 "# ctxloom-bundle-manifest/1\n# version: 1.0.0\n" + line,
		"no version":              "# ctxloom-bundle-manifest/1\n# name: b\n" + line,
		"version before name":     "# ctxloom-bundle-manifest/1\n# version: 1.0.0\n# name: b\n" + line,
		"loose version":           "# ctxloom-bundle-manifest/1\n# name: b\n# version: v1.0.0\n" + line,
		"two-part version":        "# ctxloom-bundle-manifest/1\n# name: b\n# version: 1.0\n" + line,
		"unsorted retracts":       "# ctxloom-bundle-manifest/1\n# name: b\n# version: 2.0.0\n# retracts: 1.2.0 x\n# retracts: 1.1.0 y\n" + line,
		"withdrawn before retract": "# ctxloom-bundle-manifest/1\n# name: b\n# version: 2.0.0\n# withdrawn: w\n# retracts: 1.1.0 y\n" + line,
		"unknown header":          "# ctxloom-bundle-manifest/1\n# name: b\n# version: 1.0.0\n# signer: me\n" + line,
		"trailing space":          "# ctxloom-bundle-manifest/1\n# name: b \n# version: 1.0.0\n" + line,
		"header after entries":    "# ctxloom-bundle-manifest/1\n# name: b\n# version: 1.0.0\n" + line + "# withdrawn: w\n",
		"no entries":              "# ctxloom-bundle-manifest/1\n# name: b\n# version: 1.0.0\n",
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParseManifest([]byte(raw))
			assert.ErrorIs(t, err, ErrManifestFormat)
		})
	}
}

// A per-form content digest shares the SHA256SUMS line shape, so without its
// own marker a signature over a form digest could be replayed as a bundle
// manifest. The two markers keep them apart.
func TestParseManifest_AFormDigestNeverParsesAsAManifest(t *testing.T) {
	digest, err := Digest([]Component{{Path: "fragments/a.md", Bytes: []byte("hello")}})
	require.NoError(t, err)
	_, err = ParseManifest(digest)
	assert.ErrorIs(t, err, ErrManifestFormat)
}

func TestBuildManifest_RefusesAReleaseNamedForAnotherBundle(t *testing.T) {
	_, b := openFixtureBundle(t)
	_, err := BuildManifest(context.Background(), b, testRelease(t, "someone-else", "1.0.0"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "someone-else")
}

// The header rides on `#` lines precisely so a consumer with no ctxloom can
// still check a pulled tree with stock tooling.
func TestManifest_StockSha256sumStillChecksIt(t *testing.T) {
	tool, err := exec.LookPath("sha256sum")
	if err != nil {
		t.Skip("sha256sum is not installed here")
	}
	dir := t.TempDir()
	body := []byte("hello\n")
	require.NoError(t, os.MkdirAll(filepath.Join(dir, "fragments"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(dir, "fragments", "a.md"), body, 0o600))
	rel := release.Release{Name: "b", Version: testVersion(t, "1.3.0"),
		Retracts:  []release.Retraction{{Version: testVersion(t, "1.1.0"), Reason: "leaked token"}},
		Withdrawn: "superseded"}
	m, err := NewManifest(rel, []ManifestEntry{{Path: "fragments/a.md", SHA256: SHA256Hex(body)}})
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(dir, ManifestPath), m.Bytes(), 0o600))

	for _, args := range [][]string{{"-c", ManifestPath}, {"--strict", "-c", ManifestPath}} {
		cmd := exec.Command(tool, args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		assert.NoError(t, err, "%v: %s", args, out)
		assert.Contains(t, string(out), "fragments/a.md: OK")
	}
}
