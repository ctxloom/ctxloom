package companions

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/stretchr/testify/assert"
	"golang.org/x/crypto/ssh"

	"github.com/ctxloom/ctxloom/internal/adapters/signing"
	"github.com/ctxloom/ctxloom/internal/adapters/signing/allowedsigners"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// Companion EXEC consent. Every assertion here is about OBSERVABLE BEHAVIOR —
// which binaries were admitted, what got written to the record, what the human
// was told — never about a nil error. The failure mode this gate closes is
// precisely the one an exit-code assertion cannot see.

// consentFixture wires a hermetic admission world: a temp dir for binaries on
// PATH, a temp record path for the refusal store, and a trust root holding one
// generated key that vouches for whatever this fixture signs.
//
// It carries no prompt and no install directory any more. Both belonged to the
// mechanisms admission replaced — trust-on-first-use asked a human, and the
// location pin exempted binaries sitting beside the running ctxloom. A
// signature answers the question they were approximating, so the fixture's job
// is now to sign, or deliberately not to.
type consentFixture struct {
	elsewhere string
	warnLog   *bytes.Buffer
	signer    ssh.Signer
	root      signing.TrustRoot
}

// sign vouches for the bytes at path with this fixture's key, which its trust
// root authorizes for the companion namespace. A binary this is NOT called on
// is refused as unsigned — which several tests below rely on.
func (f *consentFixture) sign(t *testing.T, path string) {
	t.Helper()
	payload, err := os.ReadFile(path)
	require.NoError(t, err)
	sig, err := signing.Sign(payload, f.signer, signing.NamespaceCompanion)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path+".sig", sig, 0o600))
}

// admit runs the real gate against this fixture's trust root.
func (f *consentFixture) admit(bins []string) []CompanionAdmission {
	return AdmitCompanions(bins, f.root)
}

func newConsentFixture(t *testing.T) *consentFixture {
	t.Helper()
	_, priv, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	signer, err := ssh.NewSignerFromSigner(priv)
	require.NoError(t, err)

	f := &consentFixture{
		elsewhere: t.TempDir(),
		warnLog:   &bytes.Buffer{},
		signer:    signer,
		root:      fixtureTrustRoot(t, signer),
	}

	restoreSink := clidiag.SetSink(f.warnLog)
	t.Cleanup(restoreSink)

	// lookPath resolves whatever the test wrote into either directory.
	prevLook := SetLookPathForTesting(func(bin string) (string, error) {
		for _, dir := range []string{f.elsewhere} {
			p := filepath.Join(dir, bin)
			if _, err := os.Stat(p); err == nil {
				return p, nil
			}
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

// --- The store's own fault path --------------------------------------------

// --- Identity resolution ---------------------------------------------------

// TestAdmitCompanions_MissingBinaryIsSilentlyNotInstalled: most machines have
// no reprise, and that must not produce a warning.
func TestAdmitCompanions_MissingBinaryIsSilentlyNotInstalled(t *testing.T) {
	f := newConsentFixture(t)

	got := admissionFor(t, f.admit([]string{"reprise"}), "reprise")
	assert.False(t, got.Allow)
	assert.Equal(t, CompanionAdmissionNotInstalled, got.Reason)
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

	got, err := Prober{}.ProbeCompanionLoadouts(context.Background(), f.root)
	require.NoError(t, err)
	assert.Empty(t, got.Loadouts)
	assert.Empty(t, execed, "an unsigned companion must never be exec'd, not merely have its output discarded")

	// Now VOUCH for it and prove the SAME fixture does run — otherwise the
	// assertion above would also pass against a probe that is simply broken.
	f.sign(t, acmePath)
	_, _ = Prober{}.ProbeCompanionLoadouts(context.Background(), f.root)
	assert.Len(t, execed, 1, "once signed by a trusted key the very same companion is exec'd")
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
	for _, st := range (Prober{}).ProbeCompanions(f.root) {
		if st.Bin == "ctxloom-companion-acme" {
			acme = st
		}
	}
	require.Equal(t, "ctxloom-companion-acme", acme.Bin, "the refused companion must still be REPORTED")
	assert.Equal(t, path, acme.Path, "the report must name the file it refused, not pretend nothing is there")
	assert.Equal(t, CompanionAdmissionUnsigned, acme.Admission)
	assert.False(t, acme.Executed())
	assert.Empty(t, execed, "the version probe is an exec too, and must not run without a valid signature")
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
	statuses := Prober{}.ProbeCompanions(f.root)
	probe, err := Prober{}.ProbeCompanionLoadouts(context.Background(), f.root)
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
	require.Equal(t, CompanionAdmissionUnsigned, acme.Admission)

	assert.NoFileExists(t, sentinel,
		"an unadmitted companion must never RUN, whatever the report says about it")

	// POSITIVE CONTROL. Vouch for it and prove the very same binary, fixture
	// and sentinel do fire — otherwise the assertion above would prove only
	// that this test is incapable of executing anything.
	f.sign(t, filepath.Join(f.elsewhere, "ctxloom-companion-acme"))
	Prober{}.ProbeCompanions(f.root)
	_, err = Prober{}.ProbeCompanionLoadouts(context.Background(), f.root)
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

	probe, err := Prober{}.ProbeCompanionLoadouts(context.Background(), f.root)
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
		"the candidate must name the file 'ctxloom companion trust' has to be pointed at")

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

// fixtureTrustRoot is a trust root holding exactly one principal: the fixture's
// own key, authorized for the companion namespace and nothing else. Scoped that
// tightly on purpose — a root that trusted the namespace broadly would admit
// binaries a test never vouched for, and the refusals below would stop meaning
// anything.
func fixtureTrustRoot(t *testing.T, signer ssh.Signer) signing.TrustRoot {
	t.Helper()
	line := fmt.Sprintf("fixture@testenv.invalid namespaces=%q %s %s\n",
		signing.NamespaceCompanion,
		signer.PublicKey().Type(),
		base64.StdEncoding.EncodeToString(signer.PublicKey().Marshal()))
	path := filepath.Join(t.TempDir(), "allowed_signers")
	require.NoError(t, os.WriteFile(path, []byte(line), 0o600))

	store, parseErrs, err := allowedsigners.ParseFile(path)
	require.NoError(t, err)
	require.Empty(t, parseErrs, "the fixture trust root must parse cleanly, or every admission below is decided by an accident")
	return store
}
