package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// U009-F01 / U096-F08: the pre-build schema gate validated ZERO files in CI, on
// every run, and exited 0. `.ctxloom/config.yaml` is gitignored, so it does not
// exist in a fresh clone; the gate found nothing to read, printed "No files
// found to validate" to stdout, and returned nil. The one thing it exists to do
// it has structurally never done — and on a developer's machine, where the file
// IS present, the same command validates one file and looks like it works.
//
// The discriminator: an absent project config is legitimately nothing to do,
// but a gate that validated nothing at all has not gated anything. Making the
// tool able to tell those apart is the fix: the tracked documents the repo
// really does ship are REQUIRED targets, the gitignored project config is an
// optional one, and a run that validates zero documents is a failure.

// TestValidateAll_ZeroDocumentsIsAFailure pins the core of U009-F01: reporting
// success having validated nothing is the silent no-op.
func TestValidateAll_ZeroDocumentsIsAFailure(t *testing.T) {
	dir := t.TempDir()
	n, err := validateAll([]target{{path: filepath.Join(dir, "absent.yaml"), optional: true}})
	assert.Zero(t, n)
	require.Error(t, err, "a gate that validated nothing must not report success")
	assert.Contains(t, err.Error(), "validated no documents")
}

// TestValidateAll_MissingRequiredTargetFails: a required document that is not
// there is a broken gate, not an empty one — the gate's inputs are missing and
// it must say so rather than quietly checking one fewer file.
func TestValidateAll_MissingRequiredTargetFails(t *testing.T) {
	dir := t.TempDir()
	good := filepath.Join(dir, "ok.yaml")
	require.NoError(t, os.WriteFile(good, []byte("{}"), 0o644))

	_, err := validateAll([]target{
		{path: good},
		{path: filepath.Join(dir, "gone.yaml")},
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "gone.yaml")
}

// TestValidateAll_UnreadableOptionalTargetFails is U009-F02: `err == nil`
// treated EVERY read failure as "the file isn't there", so a config that exists
// but cannot be read was skipped exactly like an absent one — and if it was the
// only candidate the gate reported "no files found" and exited 0.
func TestValidateAll_UnreadableOptionalTargetFails(t *testing.T) {
	dir := t.TempDir()
	// A directory at the config's path: present, and os.ReadFile fails with
	// something that is emphatically not fs.ErrNotExist.
	unreadable := filepath.Join(dir, "config.yaml")
	require.NoError(t, os.Mkdir(unreadable, 0o755))

	_, err := validateAll([]target{{path: unreadable, optional: true}})
	require.Error(t, err, "an unreadable document must not be silently skipped as absent")
	assert.NotContains(t, err.Error(), "validated no documents",
		"the failure must name the read error, not degrade to the empty-gate message")
}

// TestDefaultTargets_CoversTheTrackedDocuments: the gate is only non-empty in CI
// because it checks documents that are actually IN the repo. These three are
// tracked, are what `ctxloom init` writes and what the docs show, and are
// validated against the same embedded schema — so a change that breaks them now
// fails the build instead of shipping.
func TestDefaultTargets_CoversTheTrackedDocuments(t *testing.T) {
	var required []string
	for _, tg := range defaultTargets() {
		if !tg.optional {
			required = append(required, tg.path)
		}
	}
	assert.ElementsMatch(t, []string{
		filepath.Join("resources", "default-config.yaml"),
		filepath.Join("resources", "example-config.yaml"),
		filepath.Join("resources", "init-config.yaml"),
	}, required, "the gate's required inputs are the tracked config documents")
}

// TestTrackedDocumentsValidate runs the real gate over the repo's real
// documents (the package's own CWD is cmd/validate, hence ../..) — the check
// that has never actually run in CI.
func TestTrackedDocumentsValidate(t *testing.T) {
	var targets []target
	for _, tg := range defaultTargets() {
		if tg.optional {
			continue
		}
		targets = append(targets, target{path: filepath.Join("..", "..", tg.path)})
	}
	n, err := validateAll(targets)
	require.NoError(t, err)
	assert.Equal(t, len(targets), n)
}
