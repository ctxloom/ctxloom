package remotetree

import (
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// installedTree lays a bundle tree out on a REAL filesystem at the modes a git
// checkout would produce, which is the input applyDeclaredModesOnDisk exists to
// correct. afero's memory filesystem cannot be used here: the whole subject is
// POSIX permission bits.
func installedTree(t *testing.T, files map[string]struct {
	body string
	mode os.FileMode
},
) string {
	t.Helper()
	dir := t.TempDir()
	for rel, f := range files {
		full := filepath.Join(dir, filepath.FromSlash(rel))
		require.NoError(t, os.MkdirAll(filepath.Dir(full), 0o755))
		require.NoError(t, os.WriteFile(full, []byte(f.body), 0o644))
		require.NoError(t, os.Chmod(full, f.mode))
	}
	return dir
}

const declaringSidecar = "executable:\n  - scripts/declared.sh\n"

// TestApplyDeclaredModes_TheDeclarationWinsOverTheCommittedMode is the
// divergence this step exists for, in both directions.
//
// ctxloom publishes every file 0644, so a checkout reproduces 0644 even for a
// skill's scripts — and a skill is delivered at the mode found on disk. The
// declaration reaching disk is what makes the two agree.
func TestApplyDeclaredModes_TheDeclarationWinsOverTheCommittedMode(t *testing.T) {
	dir := installedTree(t, map[string]struct {
		body string
		mode os.FileMode
	}{
		"bundle.yaml":                           {"version: \"1.0.0\"\n", 0o644},
		"skills/.reviewer.meta.yaml":            {declaringSidecar, 0o644},
		"skills/reviewer/scripts/declared.sh":   {"#!/bin/sh\n", 0o644},
		"skills/reviewer/scripts/undeclared.sh": {"#!/bin/sh\n", 0o755},
	})

	require.NoError(t, applyDeclaredModesOnDisk(dir, "bundles/v2/atelier"))

	declared, err := os.Stat(filepath.Join(dir, "skills", "reviewer", "scripts", "declared.sh"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o755), declared.Mode().Perm(),
		"a DECLARED executable must get its bit even though the commit recorded 0644 — which is every file ctxloom publishes")

	undeclared, err := os.Stat(filepath.Join(dir, "skills", "reviewer", "scripts", "undeclared.sh"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), undeclared.Mode().Perm(),
		"a file committed executable but never declared must land non-executable, or the tree and its manifest disagree")

	plain, err := os.Stat(filepath.Join(dir, "bundle.yaml"))
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o644), plain.Mode().Perm())
}

// TestApplyDeclaredModes_SaysWhenACommittedExecutableIsUndeclared. Applying the
// declaration makes everything downstream CONSISTENT — the file is 0644, the
// manifest says 0644, verification passes — and therefore silent: the model is
// handed a script it cannot run and nothing reports a failure.
func TestApplyDeclaredModes_SaysWhenACommittedExecutableIsUndeclared(t *testing.T) {
	var warnings bytes.Buffer
	t.Cleanup(clidiag.SetSink(&warnings))

	dir := installedTree(t, map[string]struct {
		body string
		mode os.FileMode
	}{
		"skills/.reviewer.meta.yaml":            {declaringSidecar, 0o644},
		"skills/reviewer/scripts/declared.sh":   {"#!/bin/sh\n", 0o755},
		"skills/reviewer/scripts/undeclared.sh": {"#!/bin/sh\n", 0o755},
	})

	require.NoError(t, applyDeclaredModesOnDisk(dir, "bundles/v2/atelier"))

	got := warnings.String()
	assert.Contains(t, got, "bundles/v2/atelier/skills/reviewer/scripts/undeclared.sh",
		"the warning must name the file as the PUBLISHER sees it, not as a cache path they have never heard of")
	assert.Contains(t, got, "DECLARED NON-EXECUTABLE")
	assert.Contains(t, got, "executable:", "and the declaration to add")
	// "scripts/declared.sh" cannot match inside "scripts/undeclared.sh": the
	// slash pins the boundary that a bare "declared.sh" would let slide.
	assert.NotContains(t, got, "scripts/declared.sh",
		"a file whose declaration and committed mode agree has nothing to report")
}

// TestApplyDeclaredModes_RefusesAMalformedSidecar. Shrugging one off would
// resolve every file in that package to non-executable, installing a skill whose
// scripts silently do not run — the failure the declaration path exists to
// prevent.
func TestApplyDeclaredModes_RefusesAMalformedSidecar(t *testing.T) {
	dir := installedTree(t, map[string]struct {
		body string
		mode os.FileMode
	}{
		"skills/.reviewer.meta.yaml":          {"executable: [unterminated\n", 0o644},
		"skills/reviewer/scripts/declared.sh": {"#!/bin/sh\n", 0o644},
	})

	err := applyDeclaredModesOnDisk(dir, "bundles/v2/atelier")

	require.Error(t, err, "a sidecar that cannot be read must not be treated as declaring nothing")
}
