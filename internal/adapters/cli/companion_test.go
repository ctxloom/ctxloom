// Tests for companion.go's `companion show` — the read-one gap-fill:
// companion previously had `list` and no way to inspect ONE binary's
// exec-consent decision without scanning the whole listing by eye.
package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/companions"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// writeFakeCompanionBinary drops a real, executable file the exec-consent
// cascade can stat/hash — AdmitCompanions resolves symlinks and reads the
// file's bytes, so a bare fake path is not enough.
func writeFakeCompanionBinary(t *testing.T, name string) string {
	t.Helper()
	dir := t.TempDir()
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, []byte("#!/bin/sh\necho fake\n"), 0o755)) //nolint:gosec // fixture companion binary
	return p
}

func TestRunCompanionShow_UnsignedIsReportedAsUnsigned(t *testing.T) {
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
	assert.Contains(t, output, "unsigned")
}

// TestRunCompanionShow_TrustedThenShown proves show's answer agrees with
// what `companion trust` just recorded — the same decision cascade the real
// probes consult (config.AdmitCompanions), not a second, potentially
// diverging implementation.
func TestRunCompanionShow_TrustedThenShown(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	bin := writeFakeCompanionBinary(t, "acme-tool")
	restore := companions.SetLookPathForTesting(func(name string) (string, error) {
		if name == "acme-tool" || name == bin {
			return bin, nil
		}
		return "", os.ErrNotExist
	})
	t.Cleanup(restore)

	// Signed, and its key trusted for the companion namespace in this test's
	// own HOME trust root — the fixture form of a publisher vouching for the
	// bytes, which is the only thing that admits a companion now.
	testsupport.SignCompanionForTesting(t, bin,
		filepath.Join(os.Getenv("HOME"), ".ctxloom", "allowed_signers"))

	cmd, out := textCmd()
	require.NoError(t, runCompanionShowCmd(cmd, []string{"acme-tool"}))
	output := out.String()
	assert.Contains(t, output, "allowed")
	assert.Contains(t, output, "signed")
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
