package companions

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/shared/admission"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// Companion EXEC admission. Every assertion here is about OBSERVABLE
// BEHAVIOR — which binaries were admitted, what the human was told — never
// about a nil error. The failure mode this gate closes is precisely the one an
// exit-code assertion cannot see.

// consentFixture wires a hermetic admission world: HOME under a temp root (so
// the allow store and the pin directory are the test's own), a temp dir for
// binaries on PATH, and a captured warning sink.
type consentFixture struct {
	elsewhere string
	warnLog   *bytes.Buffer
}

// allow records the binary at path, as its bytes are now, in the allow store
// — what `ctxloom companion allow <path> --yes` does. A binary this is NOT
// called on is refused as not allowed, which several tests below rely on.
func (f *consentFixture) allow(t *testing.T, path string) CompanionKey {
	t.Helper()
	store, err := NewAllowStore(afero.NewOsFs())
	require.NoError(t, err)
	key, err := ResolveCompanion(path)
	require.NoError(t, err)
	_, err = store.Set(key, true)
	require.NoError(t, err)
	return key
}

// snapshot loads the allow store as it is on disk now.
func (f *consentFixture) snapshot(t *testing.T) *admission.Snapshot[CompanionKey] {
	t.Helper()
	store, err := NewAllowStore(afero.NewOsFs())
	require.NoError(t, err)
	snap, err := store.Load()
	require.NoError(t, err)
	return snap
}

// admit runs the real gate against the allow store as it is on disk now.
func (f *consentFixture) admit(t *testing.T, bins []string) []CompanionAdmission {
	t.Helper()
	return AdmitCompanions(bins, f.snapshot(t))
}

func newConsentFixture(t *testing.T) *consentFixture {
	t.Helper()
	t.Setenv("HOME", t.TempDir())
	f := &consentFixture{elsewhere: t.TempDir(), warnLog: &bytes.Buffer{}}

	restoreSink := clidiag.SetSink(f.warnLog)
	t.Cleanup(restoreSink)

	prevLook := SetLookPathForTesting(func(bin string) (string, error) {
		p := filepath.Join(f.elsewhere, bin)
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
		return "", os.ErrNotExist
	})
	t.Cleanup(prevLook)
	return f
}

// writeBin drops an executable file with the given bytes and returns its path.
func (f *consentFixture) writeBin(t *testing.T, dir, name, body string) string {
	t.Helper()
	p := filepath.Join(dir, name)
	require.NoError(t, os.WriteFile(p, []byte(body), 0o755)) //nolint:gosec // a fake companion must be executable
	return p
}

func admissionFor(t *testing.T, admissions []CompanionAdmission, bin string) CompanionAdmission {
	t.Helper()
	for _, a := range admissions {
		if a.Bin == bin {
			return a
		}
	}
	t.Fatalf("no admission decision for %q in %+v", bin, admissions)
	return CompanionAdmission{}
}

// --- The decision ----------------------------------------------------------

// TestAdmitCompanions_AnAllowedPathAndHashIsAdmitted: a record for exactly
// this path and these bytes admits, and carries the hash it was decided over.
func TestAdmitCompanions_AnAllowedPathAndHashIsAdmitted(t *testing.T) {
	f := newConsentFixture(t)
	key := f.allow(t, f.writeBin(t, f.elsewhere, "ltk", "#!/bin/sh\n"))

	got := admissionFor(t, f.admit(t, []string{"ltk"}), "ltk")
	assert.True(t, got.Allow)
	assert.Equal(t, CompanionAllowed, got.Reason)
	assert.Equal(t, key.SHA256, got.SHA256)
	assert.Empty(t, f.warnLog.String(), "an allowed companion is not news")
}

// TestAdmitCompanions_AnUnrecordedBinaryIsNotAllowed: with no record, the
// binary is refused and the warning names the command that allows it.
func TestAdmitCompanions_AnUnrecordedBinaryIsNotAllowed(t *testing.T) {
	f := newConsentFixture(t)
	path := f.writeBin(t, f.elsewhere, "ltk", "#!/bin/sh\n")

	got := admissionFor(t, f.admit(t, []string{"ltk"}), "ltk")
	assert.False(t, got.Allow)
	assert.Equal(t, CompanionNotAllowed, got.Reason)
	assert.Equal(t, path, got.Path, "the refusal names the file it refused")
	assert.NotEmpty(t, got.SHA256, "the refusal carries the hash an allow would record")
	assert.Contains(t, f.warnLog.String(), "ctxloom companion allow "+path)
}

// TestAdmitCompanions_ARebuiltBinaryIsHashChanged: the path is allowed but the
// bytes are not the ones allowed. That is its own reason, and the disclosure
// names both hashes, old -> new, so a human can tell a rebuild from a swap.
func TestAdmitCompanions_ARebuiltBinaryIsHashChanged(t *testing.T) {
	f := newConsentFixture(t)
	path := f.writeBin(t, f.elsewhere, "ltk", "#!/bin/sh\necho one\n")
	old := f.allow(t, path)
	f.writeBin(t, f.elsewhere, "ltk", "#!/bin/sh\necho two\n")

	got := admissionFor(t, f.admit(t, []string{"ltk"}), "ltk")
	assert.False(t, got.Allow)
	assert.Equal(t, CompanionHashChanged, got.Reason)
	require.NotEqual(t, old.SHA256, got.SHA256)
	assert.Contains(t, got.Detail, old.SHA256+" -> "+got.SHA256)
	assert.Contains(t, f.warnLog.String(), old.SHA256+" -> "+got.SHA256)
	assert.Contains(t, f.warnLog.String(), "ctxloom companion allow "+path)
}

// TestAdmitCompanions_AnAllowForAnotherPathDoesNotAdmit: identical bytes at a
// path nobody allowed are refused. The path is half of what a human approved.
func TestAdmitCompanions_AnAllowForAnotherPathDoesNotAdmit(t *testing.T) {
	f := newConsentFixture(t)
	other := t.TempDir()
	f.allow(t, f.writeBin(t, other, "ltk", "#!/bin/sh\n"))
	f.writeBin(t, f.elsewhere, "ltk", "#!/bin/sh\n")

	got := admissionFor(t, f.admit(t, []string{"ltk"}), "ltk")
	assert.False(t, got.Allow)
	assert.Equal(t, CompanionNotAllowed, got.Reason)
}

// TestAdmitCompanions_AnUnreadableStoreAdmitsNothing: a nil snapshot (the
// store could not be read) refuses every present binary.
func TestAdmitCompanions_AnUnreadableStoreAdmitsNothing(t *testing.T) {
	f := newConsentFixture(t)
	f.allow(t, f.writeBin(t, f.elsewhere, "ltk", "#!/bin/sh\n"))

	got := admissionFor(t, AdmitCompanions([]string{"ltk"}, nil), "ltk")
	assert.False(t, got.Allow)
	assert.Equal(t, CompanionNotAllowed, got.Reason)
}

// TestSelfAdmission_IsAllowedAsSelf: the running binary is admitted by
// identity, under its own reason, with no record consulted.
func TestSelfAdmission_IsAllowedAsSelf(t *testing.T) {
	got := selfAdmission("/opt/build/ctxloom")
	assert.True(t, got.Allow)
	assert.Equal(t, CompanionSelf, got.Reason)
	assert.Equal(t, SelfCompanion, got.Bin)
}

// --- The pin directory -----------------------------------------------------

// pinnedCopy writes body under the home pin directory as name — what
// PinAdmittedCompanions leaves there — and points lookPath at it.
func (f *consentFixture) pinnedCopy(t *testing.T, name, body string) string {
	t.Helper()
	pinRoot, err := paths.HomeCompanionPinDir()
	require.NoError(t, err)
	dir := filepath.Join(pinRoot, "digest")
	require.NoError(t, os.MkdirAll(dir, 0o755))
	p := f.writeBin(t, dir, name, body)
	t.Cleanup(SetLookPathForTesting(func(bin string) (string, error) {
		q := filepath.Join(dir, bin)
		if _, err := os.Stat(q); err != nil {
			return "", err
		}
		return q, nil
	}))
	return p
}

// TestAdmitCompanions_APinnedCopyOfAllowedBytesIsAdmitted: a ctxloom started
// from the engine's PATH finds the pinned copy, at a path nobody allowed. It
// is admitted because its bytes are the ones allowed under the same name.
func TestAdmitCompanions_APinnedCopyOfAllowedBytesIsAdmitted(t *testing.T) {
	f := newConsentFixture(t)
	f.allow(t, f.writeBin(t, f.elsewhere, "ltk", "#!/bin/sh\necho ltk\n"))
	f.pinnedCopy(t, "ltk", "#!/bin/sh\necho ltk\n")

	got := admissionFor(t, f.admit(t, []string{"ltk"}), "ltk")
	assert.True(t, got.Allow, "%s: %s", got.Reason, got.Detail)
	assert.Equal(t, CompanionAllowed, got.Reason)
}

// TestAdmitCompanions_APinnedCopyOfOtherBytesIsRefused: a file under the pin
// directory is not admitted by location. Bytes no record holds are refused.
func TestAdmitCompanions_APinnedCopyOfOtherBytesIsRefused(t *testing.T) {
	f := newConsentFixture(t)
	f.allow(t, f.writeBin(t, f.elsewhere, "ltk", "#!/bin/sh\necho ltk\n"))
	f.pinnedCopy(t, "ltk", "#!/bin/sh\necho planted\n")

	got := admissionFor(t, f.admit(t, []string{"ltk"}), "ltk")
	assert.False(t, got.Allow)
}

// TestAdmitCompanions_APinnedCopyUnderAnotherNameIsRefused: allowed bytes
// pinned under a different companion's name are refused, so one allowed
// program cannot run as another.
func TestAdmitCompanions_APinnedCopyUnderAnotherNameIsRefused(t *testing.T) {
	f := newConsentFixture(t)
	f.allow(t, f.writeBin(t, f.elsewhere, "taskloom", "#!/bin/sh\necho taskloom\n"))
	f.pinnedCopy(t, "ltk", "#!/bin/sh\necho taskloom\n")

	got := admissionFor(t, f.admit(t, []string{"ltk"}), "ltk")
	assert.False(t, got.Allow)
}

// --- The store's own fault path --------------------------------------------

// --- Identity resolution ---------------------------------------------------

// TestAdmitCompanions_MissingBinaryIsSilentlyNotInstalled: most machines have
// no reprise, and that must not produce a warning.
func TestAdmitCompanions_MissingBinaryIsSilentlyNotInstalled(t *testing.T) {
	f := newConsentFixture(t)

	got := admissionFor(t, f.admit(t, []string{"reprise"}), "reprise")
	assert.False(t, got.Allow)
	assert.Equal(t, CompanionNotInstalled, got.Reason)
	assert.Empty(t, got.Path)
	assert.Empty(t, f.warnLog.String(), "a companion that simply is not installed is ordinary, not a warning")
}

// --- The user-facing record surface ----------------------------------------

// --- The probes actually honour the gate -----------------------------------

// TestProbeCompanionLoadouts_NeverExecsAnUnadmittedCompanion is the assertion
// that matters most: not "the map came back empty" (a broken probe produces
// that too) but "the binary was never run". The exec seam is the witness.
func TestProbeCompanionLoadouts_NeverExecsAnUnadmittedCompanion(t *testing.T) {
	f := newConsentFixture(t)
	acmePath := f.writeBin(t, f.elsewhere, "ctxloom-companion-acme", "#!/bin/sh\n")
	restorePath := setPathDirsForTesting(t, []string{f.elsewhere, f.elsewhere})
	defer restorePath()

	var execed []string
	restoreProbe := SetCompanionLoadoutOutputForTesting(func(path string) ([]byte, error) {
		execed = append(execed, path)
		return nil, os.ErrNotExist
	})
	defer restoreProbe()

	got, err := Prober{}.ProbeCompanionLoadouts(context.Background())
	require.NoError(t, err)
	assert.Empty(t, got.Loadouts)
	assert.Empty(t, execed, "an unallowed companion must never be exec'd, not merely have its output discarded")

	// Now ALLOW it and prove the SAME fixture does run — otherwise the
	// assertion above would also pass against a probe that is simply broken.
	f.allow(t, acmePath)
	_, _ = Prober{}.ProbeCompanionLoadouts(context.Background())
	assert.Len(t, execed, 1, "once allowed the very same companion is exec'd")
}

// TestProbeCompanions_ReportsRefusalRatherThanAbsence: a refused companion is
// present on the machine, and reporting it as "not installed" would send the
// user chasing an install that already happened.
func TestProbeCompanions_ReportsRefusalRatherThanAbsence(t *testing.T) {
	f := newConsentFixture(t)
	path := f.writeBin(t, f.elsewhere, "ctxloom-companion-acme", "#!/bin/sh\n")
	restorePath := setPathDirsForTesting(t, []string{f.elsewhere, f.elsewhere})
	defer restorePath()

	var execed []string
	restoreVersion := SetCompanionVersionOutputForTesting(func(p string) ([]byte, error) {
		execed = append(execed, p)
		return []byte(`{"version":"1.0.0"}`), nil
	})
	defer restoreVersion()

	var acme CompanionStatus
	for _, st := range (Prober{}).ProbeCompanions() {
		if st.Bin == "ctxloom-companion-acme" {
			acme = st
		}
	}
	require.Equal(t, "ctxloom-companion-acme", acme.Bin, "the refused companion must still be REPORTED")
	assert.Equal(t, path, acme.Path, "the report must name the file it refused, not pretend nothing is there")
	assert.Equal(t, CompanionNotAllowed, acme.Admission)
	assert.False(t, acme.Executed())
	assert.Empty(t, execed, "the version probe is an exec too, and must not run without an allow record")
}

// TestProbes_NeverExecuteAnUnadmittedCompanion_RealBinary witnesses the same
// invariant as the two tests above at the OPERATING SYSTEM instead of at a
// package variable, and that difference is the whole reason it exists.
//
// Those two record the exec inside companionVersionOutput /
// companionLoadoutOutput — seams they have REPLACED with a fake. Such a
// recorder only sees an exec that still travels through the seam. Any code
// that runs the binary another way — an inline exec.Command, a new helper, a
// "cheap pre-check" added ahead of the gate — never consults the fake, so the
// recorder stays empty and both tests stay green while the binary really ran.
// That is measured, not supposed: an exec.Command placed in ProbeCompanions'
// refusal arm leaves both of them passing and fails only this test.
//
// So this one plants a REAL executable on PATH, leaves both exec seams at
// their production bodies, and asserts on a filesystem SENTINEL the binary
// writes when it runs. Nothing except an actual execution can create it.
func TestProbes_NeverExecuteAnUnadmittedCompanion_RealBinary(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the sentinel companion is an sh script")
	}
	f := newConsentFixture(t)
	sentinel := filepath.Join(t.TempDir(), "executed")
	// The script records WHICH probe ran it, so the positive control below can
	// prove BOTH exec paths reach the binary rather than just one of them.
	writeFakeCompanion(t, f.elsewhere, "ctxloom-companion-acme", "echo \"$1\" >> "+sentinel)
	restorePath := setPathDirsForTesting(t, []string{f.elsewhere, f.elsewhere})
	defer restorePath()

	// Nothing has vouched for it — the fail-closed shape of every agent and CI
	// run. Deliberately NO seam overrides: the probes below reach the real
	// exec.
	statuses := Prober{}.ProbeCompanions()
	probe, err := Prober{}.ProbeCompanionLoadouts(context.Background())
	require.NoError(t, err)
	assert.Empty(t, probe.Loadouts)

	// The companion has to have been FOUND and REFUSED. Without this the
	// sentinel assertion would pass just as well against a fixture whose binary
	// was never discovered at all — absence satisfying absence.
	var acme CompanionStatus
	for _, st := range statuses {
		if st.Bin == "ctxloom-companion-acme" {
			acme = st
		}
	}
	require.NotEmpty(t, acme.Path, "the refused companion must still be discovered on PATH")
	require.Equal(t, CompanionNotAllowed, acme.Admission)

	assert.NoFileExists(t, sentinel,
		"an unadmitted companion must never RUN, whatever the report says about it")

	// POSITIVE CONTROL. Allow it and prove the very same binary, fixture
	// and sentinel do fire — otherwise the assertion above would prove only
	// that this test is incapable of executing anything.
	f.allow(t, filepath.Join(f.elsewhere, "ctxloom-companion-acme"))
	Prober{}.ProbeCompanions()
	_, err = Prober{}.ProbeCompanionLoadouts(context.Background())
	require.NoError(t, err)

	ran, rerr := os.ReadFile(sentinel)
	require.NoError(t, rerr, "with consent recorded the same companion must actually run")
	assert.Contains(t, string(ran), "version", "ProbeCompanions must reach the real binary once admitted")
	assert.Contains(t, string(ran), "loadout", "ProbeCompanionLoadouts must reach the real binary once admitted")
}

// TestProbeCompanionLoadouts_RefusedCompanionBecomesAnUnconsentedCandidate is
// the identity-without-content case, proved on a REAL binary with the exec
// seams left at their production bodies.
//
// A companion that is present and never approved contributes nothing, and the
// only record that it exists at all is the candidate. Minting its identity
// takes the name a directory entry already gave, so the whole classification
// happens without the file ever running — asserted here on a filesystem
// sentinel only an actual execution can create.
func TestProbeCompanionLoadouts_RefusedCompanionBecomesAnUnconsentedCandidate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the sentinel companion is an sh script")
	}
	f := newConsentFixture(t)
	sentinel := filepath.Join(t.TempDir(), "executed")
	writeFakeCompanion(t, f.elsewhere, "ctxloom-companion-acme", "echo \"$1\" >> "+sentinel)
	restorePath := setPathDirsForTesting(t, []string{f.elsewhere, f.elsewhere})
	defer restorePath()

	probe, err := Prober{}.ProbeCompanionLoadouts(context.Background())
	require.NoError(t, err)
	require.Empty(t, probe.Loadouts, "an unapproved companion contributes no content")

	byBin := make(map[string]bundles.CompanionCandidate)
	for _, c := range probe.Candidates {
		byBin[c.Bin] = c
	}
	acme, ok := byBin["ctxloom-companion-acme"]
	require.True(t, ok, "the refused companion must be REPORTED, not omitted — omission reads as 'not installed'")
	assert.Equal(t, bundles.CandidateUnconsented, acme.Reason)
	assert.Equal(t, filepath.Join(f.elsewhere, "ctxloom-companion-acme"), acme.Path,
		"the candidate must name the file 'ctxloom companion show' has to be pointed at")

	assert.NoFileExists(t, sentinel,
		"classifying a companion as a candidate must not RUN it")

	// A first-party name nothing on this machine answers to is the OTHER
	// reason, and it must not be collapsed into the one above: "install it"
	// and "allow it" are different remedies.
	reprise, ok := byBin["reprise"]
	require.True(t, ok, "guard: a name with no binary must still be reported")
	assert.Equal(t, bundles.CandidateAbsent, reprise.Reason)
	assert.Empty(t, reprise.Path)
}
