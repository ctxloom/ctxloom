// Tests for analyzeBundleReferencesWithFS — the afero-seam'd
// implementation of the orphan/missing/invalid bundle-reference
// detector behind `ctxloom remote update`. Hermetic against MemMapFs
// so the YYYY/MM/DD-style on-disk layout of `.ctxloom/profiles/*.yaml`
// can be exercised without touching the real OS.
package cmd

import (
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/remote"
)

const testAppDir = "/proj/.ctxloom"

// writeProfile drops a YAML profile under testAppDir/profiles/<name>.yaml
// containing the given bundle references. The bundles slice is the only
// field analyzeBundleReferences looks at; other fields are irrelevant.
func writeProfile(t *testing.T, fs afero.Fs, name string, bundles ...string) {
	t.Helper()
	require.NoError(t, fs.MkdirAll(filepath.Join(testAppDir, "profiles"), 0755))
	var sb []byte
	sb = append(sb, "bundles:\n"...)
	for _, b := range bundles {
		sb = append(sb, "  - "+b+"\n"...)
	}
	require.NoError(t, afero.WriteFile(fs, filepath.Join(testAppDir, "profiles", name+".yaml"), sb, 0644))
}

// writeBundle creates an empty bundle file at the local-name path the
// analyzer derives. Used to silence the "missing" detector when we want
// to test other branches.
func writeBundle(t *testing.T, fs afero.Fs, localName string) {
	t.Helper()
	parts := splitLocalName(localName)
	require.Len(t, parts, 2, "test setup: localName must be remote/name")
	dir := filepath.Join(testAppDir, "bundles", parts[0])
	require.NoError(t, fs.MkdirAll(dir, 0755))
	require.NoError(t, afero.WriteFile(fs, filepath.Join(dir, parts[1]+".yaml"), []byte(""), 0644))
}

// splitLocalName is "remote/name" → ["remote", "name"]. Inlined to avoid
// pulling in strings.SplitN noise into the test.
func splitLocalName(s string) []string {
	for i := 0; i < len(s); i++ {
		if s[i] == '/' {
			return []string{s[:i], s[i+1:]}
		}
	}
	return []string{s}
}

func newLockfile(bundles map[string]remote.LockEntry) *remote.Lockfile {
	return &remote.Lockfile{Bundles: bundles}
}

// =============================================================================
// Orphan detection: bundle in lockfile, no profile references it
// =============================================================================

func TestAnalyzeBundleReferences_OrphanDetected(t *testing.T) {
	fs := afero.NewMemMapFs()
	// No profiles reference alice/forgotten, but it's in the lockfile.
	writeProfile(t, fs, "developer", "alice/wanted")
	writeBundle(t, fs, "alice/wanted") // silence missing detector

	lock := newLockfile(map[string]remote.LockEntry{
		"alice/wanted":    {SHA: "abc"},
		"alice/forgotten": {SHA: "def"},
	})

	got := analyzeBundleReferencesWithFS(lock, fs, testAppDir)
	assert.ElementsMatch(t, []string{"alice/forgotten"}, got.Orphans)
	assert.Empty(t, got.Missing)
	assert.Empty(t, got.Invalid)
	assert.Empty(t, got.Warnings)
}

// =============================================================================
// Missing detection: profile references a bundle, lockfile doesn't have
// it, AND the file isn't installed locally either
// =============================================================================

func TestAnalyzeBundleReferences_MissingDetected(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeProfile(t, fs, "developer", "alice/ghost")
	// No writeBundle for alice/ghost — file isn't on disk.

	lock := newLockfile(nil) // empty lockfile

	got := analyzeBundleReferencesWithFS(lock, fs, testAppDir)
	assert.ElementsMatch(t, []string{"alice/ghost"}, got.Missing)
	assert.Empty(t, got.Orphans)
	assert.Empty(t, got.Invalid)
}

func TestAnalyzeBundleReferences_LocalFilePreventsMissing(t *testing.T) {
	// Bundle is referenced + not in lockfile, BUT the file exists locally
	// at the derived path. Don't flag it as missing — the user might have
	// authored it locally without running `remote lock` yet.
	fs := afero.NewMemMapFs()
	writeProfile(t, fs, "developer", "alice/local")
	writeBundle(t, fs, "alice/local")

	lock := newLockfile(nil)

	got := analyzeBundleReferencesWithFS(lock, fs, testAppDir)
	assert.Empty(t, got.Missing, "local file presence cancels the 'missing' flag")
	assert.Empty(t, got.Orphans)
}

// =============================================================================
// Invalid reference: bundle string with no "/" separator
// =============================================================================

func TestAnalyzeBundleReferences_InvalidReference(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeProfile(t, fs, "broken", "not-a-valid-ref")

	got := analyzeBundleReferencesWithFS(newLockfile(nil), fs, testAppDir)
	require.Len(t, got.Invalid, 1)
	// Message embeds both the bad ref and the source filename.
	assert.Contains(t, got.Invalid[0], "not-a-valid-ref")
	assert.Contains(t, got.Invalid[0], "broken.yaml")
}

// =============================================================================
// Item-path suffix stripped before validation: "alice/foo#fragments/x" is
// the same bundle reference as "alice/foo"
// =============================================================================

func TestAnalyzeBundleReferences_ItemPathSuffixStripped(t *testing.T) {
	fs := afero.NewMemMapFs()
	writeProfile(t, fs, "developer", "alice/foo#fragments/intro")
	writeBundle(t, fs, "alice/foo")

	lock := newLockfile(map[string]remote.LockEntry{"alice/foo": {SHA: "abc"}})

	got := analyzeBundleReferencesWithFS(lock, fs, testAppDir)
	assert.Empty(t, got.Orphans, "alice/foo is referenced via the #fragments form, not orphaned")
	assert.Empty(t, got.Missing)
	assert.Empty(t, got.Invalid, "the #suffix form is valid; only the part before # is checked")
}

// =============================================================================
// Malformed YAML profile lands as a warning, not a hard error
// =============================================================================

func TestAnalyzeBundleReferences_MalformedYAMLWarns(t *testing.T) {
	fs := afero.NewMemMapFs()
	require.NoError(t, fs.MkdirAll(filepath.Join(testAppDir, "profiles"), 0755))
	require.NoError(t, afero.WriteFile(fs, filepath.Join(testAppDir, "profiles", "broken.yaml"),
		[]byte("not: [valid: yaml: at: all"), 0644))

	// A separate well-formed profile must still be processed.
	writeProfile(t, fs, "ok", "alice/foo")
	writeBundle(t, fs, "alice/foo")

	got := analyzeBundleReferencesWithFS(newLockfile(map[string]remote.LockEntry{
		"alice/foo": {SHA: "abc"},
	}), fs, testAppDir)
	require.Len(t, got.Warnings, 1)
	assert.Contains(t, got.Warnings[0], "broken.yaml")
	assert.Contains(t, got.Warnings[0], "invalid YAML")

	// The well-formed profile still got processed — no false orphan.
	assert.Empty(t, got.Orphans)
}

// =============================================================================
// Missing profiles directory: the function must return an empty result
// without erroring (a project might have an empty .ctxloom)
// =============================================================================

func TestAnalyzeBundleReferences_NoProfilesDir(t *testing.T) {
	fs := afero.NewMemMapFs()
	// Don't create the profiles dir at all.

	got := analyzeBundleReferencesWithFS(newLockfile(map[string]remote.LockEntry{
		"alice/foo": {SHA: "abc"},
	}), fs, testAppDir)

	// Every lockfile entry becomes an orphan (no profile references anything).
	assert.ElementsMatch(t, []string{"alice/foo"}, got.Orphans)
}

// =============================================================================
// Non-yaml files in profiles dir are ignored
// =============================================================================

func TestAnalyzeBundleReferences_IgnoresNonYAMLFiles(t *testing.T) {
	fs := afero.NewMemMapFs()
	dir := filepath.Join(testAppDir, "profiles")
	require.NoError(t, fs.MkdirAll(dir, 0755))
	// Drop a README and a script alongside — analyzer must skip them.
	require.NoError(t, afero.WriteFile(fs, filepath.Join(dir, "README.md"), []byte("# notes"), 0644))
	require.NoError(t, afero.WriteFile(fs, filepath.Join(dir, "helper.sh"), []byte("#!/bin/sh"), 0755))
	writeProfile(t, fs, "developer", "alice/foo")
	writeBundle(t, fs, "alice/foo")

	got := analyzeBundleReferencesWithFS(newLockfile(map[string]remote.LockEntry{
		"alice/foo": {SHA: "abc"},
	}), fs, testAppDir)
	assert.Empty(t, got.Warnings, "non-YAML files must be silently skipped (no warning)")
	assert.Empty(t, got.Invalid)
	assert.Empty(t, got.Orphans)
}

// =============================================================================
// Empty bundle string in profile is skipped (no spurious invalid)
// =============================================================================

func TestAnalyzeBundleReferences_EmptyBundleStringSkipped(t *testing.T) {
	fs := afero.NewMemMapFs()
	// An empty list entry should not surface as an invalid reference.
	writeProfile(t, fs, "developer", "", "alice/foo")
	writeBundle(t, fs, "alice/foo")

	got := analyzeBundleReferencesWithFS(newLockfile(map[string]remote.LockEntry{
		"alice/foo": {SHA: "abc"},
	}), fs, testAppDir)
	assert.Empty(t, got.Invalid, "empty bundle ref must be skipped, not flagged invalid")
}
