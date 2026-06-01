// Tests for AnalyzeBundleReferences — the afero-seam'd orphan/missing/invalid
// bundle-reference detector behind `ctxloom remote update`. Hermetic against
// MemMapFs so the .ctxloom/profiles/*.yaml layout is exercised without touching
// the real OS.
package operations

import (
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/remote"
)

const refsTestAppDir = "/proj/.ctxloom"

func analyze(fs afero.Fs, lock *remote.Lockfile) *BundleAnalysis {
	return AnalyzeBundleReferences(AnalyzeBundleReferencesRequest{Lockfile: lock, FS: fs, AppDir: refsTestAppDir})
}

// writeRefProfile drops a YAML profile with the given bundle references.
func writeRefProfile(t *testing.T, fs afero.Fs, name string, bundles ...string) {
	t.Helper()
	require.NoError(t, fs.MkdirAll(filepath.Join(refsTestAppDir, "profiles"), 0755))
	sb := []byte("bundles:\n")
	for _, b := range bundles {
		sb = append(sb, "  - "+b+"\n"...)
	}
	require.NoError(t, afero.WriteFile(fs, filepath.Join(refsTestAppDir, "profiles", name+".yaml"), sb, 0644))
}

// writeRefBundle creates an empty bundle file at the local-name path the analyzer
// derives (silences the "missing" detector).
func writeRefBundle(t *testing.T, fs afero.Fs, localName string) {
	t.Helper()
	parts := splitRefLocalName(localName)
	require.Len(t, parts, 2, "test setup: localName must be remote/name")
	dir := filepath.Join(refsTestAppDir, "bundles", parts[0])
	require.NoError(t, fs.MkdirAll(dir, 0755))
	require.NoError(t, afero.WriteFile(fs, filepath.Join(dir, parts[1]+".yaml"), []byte(""), 0644))
}

func splitRefLocalName(s string) []string {
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			return []string{s[:i], s[i+1:]}
		}
	}
	return []string{s}
}

func refLockfile(bundles map[string]remote.LockEntry) *remote.Lockfile {
	return &remote.Lockfile{Bundles: bundles}
}

func TestAnalyzeBundleReferences_OrphanDetected(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeRefProfile(t, fs, "developer", "alice/wanted")
	writeRefBundle(t, fs, "alice/wanted")

	got := analyze(fs, refLockfile(map[string]remote.LockEntry{
		"alice/wanted":    {SHA: "abc"},
		"alice/forgotten": {SHA: "def"},
	}))
	assert.ElementsMatch(t, []string{"alice/forgotten"}, got.Orphans)
	assert.Empty(t, got.Missing)
	assert.Empty(t, got.Invalid)
	assert.Empty(t, got.Warnings)
}

func TestAnalyzeBundleReferences_MissingDetected(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeRefProfile(t, fs, "developer", "alice/ghost")

	got := analyze(fs, refLockfile(nil))
	assert.ElementsMatch(t, []string{"alice/ghost"}, got.Missing)
	assert.Empty(t, got.Orphans)
	assert.Empty(t, got.Invalid)
}

func TestAnalyzeBundleReferences_LocalFilePreventsMissing(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeRefProfile(t, fs, "developer", "alice/local")
	writeRefBundle(t, fs, "alice/local")

	got := analyze(fs, refLockfile(nil))
	assert.Empty(t, got.Missing, "local file presence cancels the 'missing' flag")
	assert.Empty(t, got.Orphans)
}

func TestAnalyzeBundleReferences_InvalidReference(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeRefProfile(t, fs, "broken", "not-a-valid-ref")

	got := analyze(fs, refLockfile(nil))
	require.Len(t, got.Invalid, 1)
	assert.Contains(t, got.Invalid[0], "not-a-valid-ref")
	assert.Contains(t, got.Invalid[0], "broken.yaml")
}

func TestAnalyzeBundleReferences_ItemPathSuffixStripped(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeRefProfile(t, fs, "developer", "alice/foo#fragments/intro")
	writeRefBundle(t, fs, "alice/foo")

	got := analyze(fs, refLockfile(map[string]remote.LockEntry{"alice/foo": {SHA: "abc"}}))
	assert.Empty(t, got.Orphans, "alice/foo is referenced via the #fragments form, not orphaned")
	assert.Empty(t, got.Missing)
	assert.Empty(t, got.Invalid, "only the part before # is checked")
}

func TestAnalyzeBundleReferences_MalformedYAMLWarns(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll(filepath.Join(refsTestAppDir, "profiles"), 0755))
	require.NoError(t, afero.WriteFile(fs, filepath.Join(refsTestAppDir, "profiles", "broken.yaml"),
		[]byte("not: [valid: yaml: at: all"), 0644))
	writeRefProfile(t, fs, "ok", "alice/foo")
	writeRefBundle(t, fs, "alice/foo")

	got := analyze(fs, refLockfile(map[string]remote.LockEntry{"alice/foo": {SHA: "abc"}}))
	require.Len(t, got.Warnings, 1)
	assert.Contains(t, got.Warnings[0], "broken.yaml")
	assert.Contains(t, got.Warnings[0], "invalid YAML")
	assert.Empty(t, got.Orphans)
}

func TestAnalyzeBundleReferences_NoProfilesDir(t *testing.T) {
	fs := afero.NewMemMapFs()
	got := analyze(fs, refLockfile(map[string]remote.LockEntry{"alice/foo": {SHA: "abc"}}))
	assert.ElementsMatch(t, []string{"alice/foo"}, got.Orphans)
}

func TestAnalyzeBundleReferences_IgnoresNonYAMLFiles(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := filepath.Join(refsTestAppDir, "profiles")
	require.NoError(t, fs.MkdirAll(dir, 0755))
	require.NoError(t, afero.WriteFile(fs, filepath.Join(dir, "README.md"), []byte("# notes"), 0644))
	require.NoError(t, afero.WriteFile(fs, filepath.Join(dir, "helper.sh"), []byte("#!/bin/sh"), 0755))
	writeRefProfile(t, fs, "developer", "alice/foo")
	writeRefBundle(t, fs, "alice/foo")

	got := analyze(fs, refLockfile(map[string]remote.LockEntry{"alice/foo": {SHA: "abc"}}))
	assert.Empty(t, got.Warnings, "non-YAML files must be silently skipped")
	assert.Empty(t, got.Invalid)
	assert.Empty(t, got.Orphans)
}

func TestAnalyzeBundleReferences_EmptyBundleStringSkipped(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeRefProfile(t, fs, "developer", "", "alice/foo")
	writeRefBundle(t, fs, "alice/foo")

	got := analyze(fs, refLockfile(map[string]remote.LockEntry{"alice/foo": {SHA: "abc"}}))
	assert.Empty(t, got.Invalid, "empty bundle ref must be skipped, not flagged invalid")
}
