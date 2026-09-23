package companions

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/adapters/signing"
	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// A companion's signature is over its RELEASE STATEMENT — name, version, and
// the hash of its bytes — not over the bytes alone. Signing bytes alone vouches
// for "some program", and a trusted publisher ships several: taskloom's
// genuine signature would admit it installed under ltk's name, where ctxloom
// then runs it as ltk.

func TestAdmitCompanions_ASignedCompanionRenamedToAnotherIsRefused(t *testing.T) {
	f := newConsentFixture(t)
	taskloom := f.writeBin(t, t.TempDir(), "taskloom", "#!/bin/sh\necho taskloom\n")
	f.sign(t, taskloom)

	ltk := f.writeBin(t, f.elsewhere, "ltk", "#!/bin/sh\necho taskloom\n")
	for _, ext := range []string{".release", ".sig"} {
		data, err := os.ReadFile(taskloom + ext)
		require.NoError(t, err)
		require.NoError(t, os.WriteFile(ltk+ext, data, 0o600))
	}

	got := admissionFor(t, f.admit([]string{"ltk"}), "ltk")
	assert.False(t, got.Allow)
	assert.Equal(t, CompanionAdmissionTampered, got.Reason)
	assert.Contains(t, f.warnLog.String(), `"taskloom"`)
	assert.Contains(t, f.warnLog.String(), `"ltk"`)
}

func TestAdmitCompanions_BytesTheStatementDoesNotHashAreRefused(t *testing.T) {
	f := newConsentFixture(t)
	path := f.writeBin(t, f.elsewhere, "ltk", "#!/bin/sh\necho good\n")
	f.sign(t, path)
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\necho evil\n"), 0o755)) //nolint:gosec // a fake companion must be executable

	got := admissionFor(t, f.admit([]string{"ltk"}), "ltk")
	assert.False(t, got.Allow)
	assert.Equal(t, CompanionAdmissionTampered, got.Reason)
}

func TestAdmitCompanions_ASignatureWithNoReleaseStatementIsUnsigned(t *testing.T) {
	f := newConsentFixture(t)
	path := f.writeBin(t, f.elsewhere, "ltk", "#!/bin/sh\n")
	// The retired shape: a signature over the raw bytes, nothing beside it.
	binary, err := os.ReadFile(path)
	require.NoError(t, err)
	sig, err := signing.Sign(binary, f.signer, signing.NamespaceCompanion)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path+".sig", sig, 0o600))

	got := admissionFor(t, f.admit([]string{"ltk"}), "ltk")
	assert.False(t, got.Allow)
	assert.Equal(t, CompanionAdmissionUnsigned, got.Reason)
}

func TestAdmitCompanions_ASignedStatementThatIsNotCanonicalIsRefused(t *testing.T) {
	f := newConsentFixture(t)
	path := f.writeBin(t, f.elsewhere, "ltk", "#!/bin/sh\n")
	binary, err := os.ReadFile(path)
	require.NoError(t, err)
	good := string(testsupport.CompanionReleaseStatement("ltk", "1.0.0", binary))
	for name, bad := range map[string]string{
		"loose version":   replaceOnce(good, "# version: 1.0.0", "# version: v1"),
		"unknown header":  replaceOnce(good, "# version: 1.0.0\n", "# version: 1.0.0\n# channel: beta\n"),
		"second entry":    good + good[len(good)-len("  ltk\n")-64:],
		"a bundle marker": replaceOnce(good, "# ctxloom-companion/1", "# ctxloom-bundle-manifest/1"),
	} {
		t.Run(name, func(t *testing.T) {
			f.signStatement(t, path, []byte(bad))
			got := admissionFor(t, f.admit([]string{"ltk"}), "ltk")
			assert.False(t, got.Allow)
			assert.Equal(t, CompanionAdmissionTampered, got.Reason)
		})
	}
}

func TestAdmitCompanions_AMatchingSignedStatementIsAdmitted(t *testing.T) {
	f := newConsentFixture(t)
	path := f.writeBin(t, f.elsewhere, "ltk", "#!/bin/sh\n")
	f.sign(t, path)
	got := admissionFor(t, f.admit([]string{"ltk"}), "ltk")
	assert.True(t, got.Allow)
	assert.Equal(t, CompanionAdmissionSigned, got.Reason)
	assert.Equal(t, filepath.Join(f.elsewhere, "ltk"), got.Path)
}

func replaceOnce(s, old, repl string) string { return strings.Replace(s, old, repl, 1) }
