package companions

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/testsupport"
	"github.com/ctxloom/ctxloom/pkg/clifmt/clidiag"
)

func TestBinaryName_FirstPartyIsItsOwnBinary_OthersTakeThePrefix(t *testing.T) {
	assert.Equal(t, "ltk", BinaryName("ltk"))
	assert.Equal(t, "taskloom", BinaryName("taskloom"))
	assert.Equal(t, "ctxloom-companion-acme", BinaryName("acme"))
}

func TestValidateName_RefusesPathsAndEmpty(t *testing.T) {
	for _, bad := range []string{"", "a/b", "../x", "/abs", `a\b`, "-flag", "has space"} {
		assert.ErrorIs(t, ValidateName(bad), ErrInvalidCompanionName, "%q", bad)
	}
	assert.NoError(t, ValidateName("acme"))
	assert.NoError(t, ValidateName("acme_2.x-y"))
}

// writeWitnessCompanion writes a real executable shell script named bin into
// dir. Every invocation appends a line to <dir>/<bin>.ran, so "was executed"
// is read off the filesystem; `loadout` prints loadout.
func writeWitnessCompanion(t *testing.T, dir, bin, loadout string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("shell-script companions")
	}
	require.NoError(t, os.MkdirAll(dir, 0o755))
	witness := filepath.Join(dir, bin+".ran")
	script := fmt.Sprintf("#!/bin/sh\necho \"$@\" >> %q\ncase \"$1\" in\n  loadout) printf '%%s' %q ;;\n  version) printf '{\"version\":\"1.0.0\"}' ;;\n  *) exit 1 ;;\nesac\n", witness, loadout)
	p := filepath.Join(dir, bin)
	require.NoError(t, os.WriteFile(p, []byte(script), 0o755)) //nolint:gosec // an executable fixture
	return witness
}

func ran(t *testing.T, witness string) bool {
	t.Helper()
	_, err := os.Stat(witness)
	if errors.Is(err, os.ErrNotExist) {
		return false
	}
	require.NoError(t, err)
	return true
}

// TestProbeCompanionLoadouts_UnregisteredOnPathIsNeverExecuted is the npm
// case: a transitive dependency ships ctxloom-companion-<anything> into an
// ABSOLUTE node_modules/.bin that npx/direnv put on PATH. Nothing registered
// that name, so it is never executed — not probed for a version, not for a
// loadout — while a registered companion in the same directory is.
func TestProbeCompanionLoadouts_UnregisteredOnPathIsNeverExecuted(t *testing.T) {
	bin := filepath.Join(t.TempDir(), "project", "node_modules", ".bin")
	lo := string(testsupport.RunLoadout("version: \"1.0.0\"\nfragments:\n  acme:\n    content: hello\n"))
	squatter := writeWitnessCompanion(t, bin, "ctxloom-companion-evil", lo)
	registered := writeWitnessCompanion(t, bin, "ctxloom-companion-acme", lo)
	t.Setenv("PATH", bin)

	probe, err := Prober{}.ProbeCompanionLoadouts(context.Background(), []string{"acme"})
	require.NoError(t, err)
	_ = Prober{}.ProbeCompanions([]string{"acme"})

	assert.False(t, ran(t, squatter), "an unregistered companion on PATH must never be executed")
	assert.True(t, ran(t, registered), "the registered companion must be executed")
	require.Len(t, probe.Loadouts, 1)
	assert.Equal(t, "ctxloom-companion-acme", probe.Loadouts[0].Bin)
	for _, c := range probe.Candidates {
		assert.NotEqual(t, "ctxloom-companion-evil", c.Bin, "an unregistered binary is not even a candidate")
	}
}

// TestProbeCompanionLoadouts_NothingRegisteredExecutesNothing: with no
// registration a first-party binary on PATH is not used either.
func TestProbeCompanionLoadouts_NothingRegisteredExecutesNothing(t *testing.T) {
	dir := t.TempDir()
	witness := writeWitnessCompanion(t, dir, "ltk", "x")
	t.Setenv("PATH", dir)

	probe, err := Prober{}.ProbeCompanionLoadouts(context.Background(), nil)
	require.NoError(t, err)
	assert.Empty(t, Prober{}.ProbeCompanions(nil))
	assert.Empty(t, probe.Loadouts)
	assert.Empty(t, probe.Candidates)
	assert.False(t, ran(t, witness))
}

// TestProbeCompanionLoadouts_RegisteredButAbsentIsAFinding: a registered name
// that resolves to nothing is reported by name, with both ways out.
func TestProbeCompanionLoadouts_RegisteredButAbsentIsAFinding(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	var buf bytes.Buffer
	t.Cleanup(clidiag.SetSink(&buf))

	probe, err := Prober{}.ProbeCompanionLoadouts(context.Background(), []string{"ghost"})
	require.NoError(t, err)
	require.Len(t, probe.Candidates, 1)
	assert.Equal(t, bundles.CandidateAbsent, probe.Candidates[0].Reason)
	assert.Equal(t, "ctxloom-companion-ghost", probe.Candidates[0].Bin)
	out := buf.String()
	assert.Contains(t, out, `"ghost"`)
	assert.Contains(t, out, "ctxloom-companion-ghost")
	assert.Contains(t, out, "ctxloom companion remove ghost")
	assert.Contains(t, out, "ctxloom companion add ghost")
}

func TestVerify_ResolvesAndAnswersTheLoadoutProbe(t *testing.T) {
	dir := t.TempDir()
	witness := writeWitnessCompanion(t, dir, "ctxloom-companion-acme", "loadout-bytes")
	t.Setenv("PATH", dir)

	got, err := Verify("acme")
	require.NoError(t, err)
	assert.Equal(t, Resolved{Name: "acme", Bin: "ctxloom-companion-acme", Path: filepath.Join(dir, "ctxloom-companion-acme")}, got)
	assert.True(t, ran(t, witness))
}

func TestVerify_NotOnPath(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	_, err := Verify("acme")
	require.ErrorIs(t, err, ErrCompanionNotOnPath)
	assert.Contains(t, err.Error(), "ctxloom-companion-acme")
}

func TestVerify_ABinaryThatDoesNotAnswerIsNotACompanion(t *testing.T) {
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, "ctxloom-companion-mute"), []byte("#!/bin/sh\nexit 3\n"), 0o755)) //nolint:gosec // an executable fixture
	t.Setenv("PATH", dir)
	_, err := Verify("mute")
	require.ErrorIs(t, err, ErrNotACompanion)

	require.NoError(t, os.WriteFile(filepath.Join(dir, "ctxloom-companion-silent"), []byte("#!/bin/sh\nexit 0\n"), 0o755)) //nolint:gosec // an executable fixture
	_, err = Verify("silent")
	require.ErrorIs(t, err, ErrNotACompanion)
	assert.True(t, strings.Contains(err.Error(), "printed no loadout"), err.Error())
}

func TestVerify_InvalidName(t *testing.T) {
	_, err := Verify("../acme")
	require.ErrorIs(t, err, ErrInvalidCompanionName)
}
