package companions

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// The pin is what an engine's PATH resolves a companion's bare name against,
// so every assertion here is about WHICH BYTES a bare name would reach — never
// about a nil error.

// TestPinAdmittedCompanions_PinsOnlyVerifiedBytesWithTheirSignature: an
// admitted companion is pinned as the exact bytes its signature covered, with
// that signature beside it; a present-but-unsigned one and an absent one are
// not pinned at all.
func TestPinAdmittedCompanions_PinsOnlyVerifiedBytesWithTheirSignature(t *testing.T) {
	f := newConsentFixture(t)
	restorePath := setPathDirsForTesting(t, []string{f.elsewhere})
	defer restorePath()
	signed := f.writeBin(t, f.elsewhere, "taskloom", "#!/bin/sh\necho admitted\n")
	f.sign(t, signed)
	f.writeBin(t, f.elsewhere, "ltk", "#!/bin/sh\necho unsigned\n")

	dir, err := PinAdmittedCompanions(t.TempDir(), f.root)
	require.NoError(t, err)
	require.NotEmpty(t, dir)

	want, err := os.ReadFile(signed)
	require.NoError(t, err)
	got, err := os.ReadFile(filepath.Join(dir, "taskloom"))
	require.NoError(t, err)
	assert.Equal(t, want, got, "the pinned taskloom is the admitted bytes")
	wantSig, err := os.ReadFile(signed + companionSigSuffix)
	require.NoError(t, err)
	gotSig, err := os.ReadFile(filepath.Join(dir, "taskloom"+companionSigSuffix))
	require.NoError(t, err)
	assert.Equal(t, wantSig, gotSig, "the signature travels with the pinned bytes")
	info, err := os.Stat(filepath.Join(dir, "taskloom"))
	require.NoError(t, err)
	assert.NotZero(t, info.Mode().Perm()&0o111, "the pinned companion is executable")

	assert.NoFileExists(t, filepath.Join(dir, "ltk"), "an unsigned companion is never pinned")
	assert.NoFileExists(t, filepath.Join(dir, "reprise"), "an absent companion is never pinned")
}

// TestPinAdmittedCompanions_IsACopyNotALink: swapping the original after
// admission must not change what the pinned name resolves to.
func TestPinAdmittedCompanions_IsACopyNotALink(t *testing.T) {
	f := newConsentFixture(t)
	restorePath := setPathDirsForTesting(t, []string{f.elsewhere})
	defer restorePath()
	orig := f.writeBin(t, f.elsewhere, "ltk", "#!/bin/sh\necho admitted\n")
	f.sign(t, orig)

	dir, err := PinAdmittedCompanions(t.TempDir(), f.root)
	require.NoError(t, err)

	require.NoError(t, os.WriteFile(orig, []byte("#!/bin/sh\necho swapped\n"), 0o755)) //nolint:gosec // a fake companion
	got, err := os.ReadFile(filepath.Join(dir, "ltk"))
	require.NoError(t, err)
	assert.Equal(t, "#!/bin/sh\necho admitted\n", string(got))
	fi, err := os.Lstat(filepath.Join(dir, "ltk"))
	require.NoError(t, err)
	assert.Zero(t, fi.Mode()&os.ModeSymlink, "a link could be swapped under the pin")
}

// TestPinAdmittedCompanions_PinnedCopyReadmits: a ctxloom started FROM the
// pinned PATH (a hook the engine fires) discovers the pinned copy first, and
// must admit it — the signature beside it covers exactly these bytes.
func TestPinAdmittedCompanions_PinnedCopyReadmits(t *testing.T) {
	f := newConsentFixture(t)
	restorePath := setPathDirsForTesting(t, []string{f.elsewhere})
	defer restorePath()
	f.sign(t, f.writeBin(t, f.elsewhere, "ltk", "#!/bin/sh\n"))

	dir, err := PinAdmittedCompanions(t.TempDir(), f.root)
	require.NoError(t, err)

	restore := SetLookPathForTesting(func(bin string) (string, error) {
		p := filepath.Join(dir, bin)
		if _, err := os.Stat(p); err != nil {
			return "", err
		}
		return p, nil
	})
	defer restore()
	got := admissionFor(t, f.admit([]string{"ltk"}), "ltk")
	assert.True(t, got.Allow, "the pinned copy re-admits: %s", got.Reason)
}

// TestPinAdmittedCompanions_ContentAddressed: the same admitted set pins to
// the same directory, so sessions share one copy rather than one each.
func TestPinAdmittedCompanions_ContentAddressed(t *testing.T) {
	f := newConsentFixture(t)
	restorePath := setPathDirsForTesting(t, []string{f.elsewhere})
	defer restorePath()
	bin := f.writeBin(t, f.elsewhere, "ltk", "#!/bin/sh\necho one\n")
	f.sign(t, bin)
	store := t.TempDir()

	first, err := PinAdmittedCompanions(store, f.root)
	require.NoError(t, err)
	again, err := PinAdmittedCompanions(store, f.root)
	require.NoError(t, err)
	assert.Equal(t, first, again)

	require.NoError(t, os.WriteFile(bin, []byte("#!/bin/sh\necho two\n"), 0o755)) //nolint:gosec // a fake companion
	f.sign(t, bin)
	next, err := PinAdmittedCompanions(store, f.root)
	require.NoError(t, err)
	assert.NotEqual(t, first, next, "different admitted bytes pin to a different directory")
}

// TestPinAdmittedCompanions_NothingAdmittedPinsNothing: no admitted companion
// means no directory to put on PATH.
func TestPinAdmittedCompanions_NothingAdmittedPinsNothing(t *testing.T) {
	f := newConsentFixture(t)
	restorePath := setPathDirsForTesting(t, []string{f.elsewhere})
	defer restorePath()

	dir, err := PinAdmittedCompanions(t.TempDir(), f.root)
	require.NoError(t, err)
	assert.Empty(t, dir)
}
