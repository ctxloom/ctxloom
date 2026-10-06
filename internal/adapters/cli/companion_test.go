// Tests for companion.go: `companion show` (one binary's admission decision),
// and `companion allow` / `companion forget`, which preview by default and
// write only with --yes.
package cli

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/companions"
	"github.com/ctxloom/ctxloom/internal/core/paths"
)

// allowWithYes runs `companion allow <target> --yes`, the way a human records
// an allow.
func allowWithYes(t *testing.T, target string) {
	t.Helper()
	setFlagForTest(t, &companionAllowYes, true)
	cmd, _ := textCmd()
	require.NoError(t, runCompanionAllowCmd(cmd, []string{target}))
}

// setFlagForTest sets a package-level flag variable for one test.
func setFlagForTest(t *testing.T, v *bool, val bool) {
	t.Helper()
	prev := *v
	*v = val
	t.Cleanup(func() { *v = prev })
}

// writeFakeCompanionBinary drops a real, executable file the admission
// cascade can stat/hash — AdmitCompanions resolves symlinks and reads the
// file's bytes, so a bare fake path is not enough.
func writeFakeCompanionBinary(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, []byte("#!/bin/sh\necho fake\n"), 0o755)) //nolint:gosec // fixture companion binary
	return p
}

func TestRunCompanionShow_UnrecordedIsReportedAsNotAllowed(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	bin := writeFakeCompanionBinary(t, "acme-tool")
	restore := companions.SetLookPathForTesting(func(name string) (string, error) {
		if name == "acme-tool" || name == bin {
			return bin, nil
		}
		return "", os.ErrNotExist
	})
	t.Cleanup(restore)

	cmd, out := textCmd()
	require.NoError(t, runCompanionShowCmd(cmd, []string{"acme-tool"}))
	output := out.String()
	assert.Contains(t, output, "acme-tool")
	assert.Contains(t, output, bin)
	assert.Contains(t, output, "not-allowed")
}

// TestRunCompanionShow_AllowedThenShown proves show's answer agrees with the
// allow just recorded — the same decision the real probes consult
// (companions.AdmitCompanions), not a second, potentially diverging
// implementation.
func TestRunCompanionShow_AllowedThenShown(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	bin := writeFakeCompanionBinary(t, "acme-tool")
	restore := companions.SetLookPathForTesting(func(name string) (string, error) {
		if name == "acme-tool" || name == bin {
			return bin, nil
		}
		return "", os.ErrNotExist
	})
	t.Cleanup(restore)

	allowWithYes(t, bin)

	cmd, out := textCmd()
	require.NoError(t, runCompanionShowCmd(cmd, []string{"acme-tool"}))
	output := out.String()
	assert.Contains(t, output, "allowed")
	assert.NotContains(t, output, "not-allowed")
}

// TestRunCompanionShow_NotOnPathReportsNotInstalled: show never conjures a
// prompt or an error for a name that resolves to nothing — "not installed"
// is the ordinary, silent case every OTHER companion surface treats it as.
func TestRunCompanionShow_NotOnPathReportsNotInstalled(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	restore := companions.SetLookPathForTesting(func(string) (string, error) {
		return "", os.ErrNotExist
	})
	t.Cleanup(restore)

	cmd, out := textCmd()
	require.NoError(t, runCompanionShowCmd(cmd, []string{"nonexistent-tool"}))
	output := out.String()
	assert.Contains(t, output, "not-installed")
}

// TestRunCompanionShow_PrintsTheAdmittedBinarysHash: show prints the sha256
// of the binary it would execute, and --format json carries it, so a user can
// check the bytes against what the publisher released.
func TestRunCompanionShow_PrintsTheAdmittedBinarysHash(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	bin := writeFakeCompanionBinary(t, "acme-tool")
	t.Cleanup(companions.SetLookPathForTesting(func(name string) (string, error) {
		if name == "acme-tool" {
			return bin, nil
		}
		return "", os.ErrNotExist
	}))
	allowWithYes(t, bin)
	raw, err := os.ReadFile(bin) //nolint:gosec // the fixture just written
	require.NoError(t, err)
	sum := sha256.Sum256(raw)
	want := hex.EncodeToString(sum[:])

	cmd, out := textCmd()
	require.NoError(t, runCompanionShowCmd(cmd, []string{"acme-tool"}))
	assert.Contains(t, out.String(), "sha256: "+want)

	jcmd, jout := formatCmd("json")
	require.NoError(t, runCompanionShowCmd(jcmd, []string{"acme-tool"}))
	var shown companionShow
	require.NoError(t, json.Unmarshal(jout.Bytes(), &shown))
	assert.Equal(t, want, shown.SHA256)
}

// TestRunCompanionAllow_PreviewWritesNothingAndYesApplies: bare `allow` prints
// the path and hash it would record and writes nothing; --yes records it.
func TestRunCompanionAllow_PreviewWritesNothingAndYesApplies(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	bin := writeFakeCompanionBinary(t, "acme-tool")
	storePath, err := paths.HomeCompanionAllowPath()
	require.NoError(t, err)

	setFlagForTest(t, &companionAllowYes, false)
	cmd, out := textCmd()
	require.NoError(t, runCompanionAllowCmd(cmd, []string{bin}))
	assert.Contains(t, out.String(), bin)
	assert.Contains(t, out.String(), "sha256: ")
	assert.Contains(t, out.String(), "--yes")
	assert.NoFileExists(t, storePath, "a preview must write nothing")

	allowWithYes(t, bin)
	assert.FileExists(t, storePath)
	cmd, out = textCmd()
	require.NoError(t, runCompanionShowCmd(cmd, []string{bin}))
	assert.NotContains(t, out.String(), "not-allowed")
}

// TestRunCompanionAllow_HashChangedPreviewNamesOldAndNew: re-allowing a
// rebuilt binary shows the recorded hash and the present one.
func TestRunCompanionAllow_HashChangedPreviewNamesOldAndNew(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	bin := writeFakeCompanionBinary(t, "acme-tool")
	allowWithYes(t, bin)
	old := sha256Of(t, bin)
	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\necho rebuilt\n"), 0o755)) //nolint:gosec // fixture companion binary

	setFlagForTest(t, &companionAllowYes, false)
	cmd, out := textCmd()
	require.NoError(t, runCompanionAllowCmd(cmd, []string{bin}))
	assert.Contains(t, out.String(), "hash changed: "+old+" -> "+sha256Of(t, bin))
}

// TestRunCompanionForget_PreviewThenYes: bare `forget` reports and keeps the
// record; --yes removes it, and the binary is not allowed afterwards.
func TestRunCompanionForget_PreviewThenYes(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	bin := writeFakeCompanionBinary(t, "acme-tool")
	allowWithYes(t, bin)

	setFlagForTest(t, &companionForgetYes, false)
	cmd, out := textCmd()
	require.NoError(t, runCompanionForgetCmd(cmd, []string{bin}))
	assert.Contains(t, out.String(), "Nothing was removed")
	cmd, out = textCmd()
	require.NoError(t, runCompanionShowCmd(cmd, []string{bin}))
	assert.NotContains(t, out.String(), "not-allowed", "a forget preview must not forget")

	setFlagForTest(t, &companionForgetYes, true)
	cmd, _ = textCmd()
	require.NoError(t, runCompanionForgetCmd(cmd, []string{bin}))
	cmd, out = textCmd()
	require.NoError(t, runCompanionShowCmd(cmd, []string{bin}))
	assert.Contains(t, out.String(), "not-allowed")
}

func sha256Of(t *testing.T, path string) string {
	t.Helper()
	raw, err := os.ReadFile(path) //nolint:gosec // a fixture path
	require.NoError(t, err)
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}
