package config

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// Companion EXEC consent. Every assertion here is about OBSERVABLE BEHAVIOR —
// which binaries were admitted, what got written to the record, what the human
// was told — never about a nil error. The failure mode this gate closes is
// precisely the one an exit-code assertion cannot see.

// consentFixture wires a hermetic consent world: a temp "install dir" standing
// in for the directory the running ctxloom lives in, a temp "elsewhere" dir for
// binaries that are merely on PATH, a temp consent-record path, and a
// non-interactive session by default. Returns the two directories.
type consentFixture struct {
	installDir string
	elsewhere  string
	recordPath string
	prompted   int
	answers    []string
	promptLog  *bytes.Buffer
	warnLog    *bytes.Buffer
}

func newConsentFixture(t *testing.T) *consentFixture {
	t.Helper()
	f := &consentFixture{
		installDir: t.TempDir(),
		elsewhere:  t.TempDir(),
		recordPath: filepath.Join(t.TempDir(), "companion_consent.yaml"),
		promptLog:  &bytes.Buffer{},
		warnLog:    &bytes.Buffer{},
	}

	prevPath := companionConsentPath
	companionConsentPath = func() (string, error) { return f.recordPath, nil }
	t.Cleanup(func() { companionConsentPath = prevPath })

	prevInstall := companionInstallDir
	companionInstallDir = func() (string, error) { return f.installDir, nil }
	t.Cleanup(func() { companionInstallDir = prevInstall })

	// Non-interactive unless a test explicitly says otherwise: that is the
	// shape of every agent and CI run, and the one that must fail closed.
	prevInteractive := companionSessionInteractive
	companionSessionInteractive = func() bool { return false }
	t.Cleanup(func() { companionSessionInteractive = prevInteractive })

	prevOut := companionPromptOut
	companionPromptOut = f.promptLog
	t.Cleanup(func() { companionPromptOut = prevOut })

	restoreSink := clidiag.SetSink(f.warnLog)
	t.Cleanup(restoreSink)

	// lookPath resolves whatever the test wrote into either directory.
	prevLook := SetLookPathForTesting(func(bin string) (string, error) {
		for _, dir := range []string{f.elsewhere, f.installDir} {
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

// interactive turns the session interactive and queues answers, one per
// prompt, counting how many questions were actually asked.
func (f *consentFixture) interactive(t *testing.T, answers ...string) {
	t.Helper()
	f.answers = answers
	companionSessionInteractive = func() bool { return true }
	prevIn := companionPromptIn
	companionPromptIn = &promptAnswerer{f: f}
	t.Cleanup(func() { companionPromptIn = prevIn })
}

// promptAnswerer feeds one queued answer per read and counts the prompts.
type promptAnswerer struct{ f *consentFixture }

func (p *promptAnswerer) Read(b []byte) (int, error) {
	if len(p.f.answers) == 0 {
		return 0, os.ErrClosed
	}
	ans := p.f.answers[0]
	p.f.answers = p.f.answers[1:]
	p.f.prompted++
	return copy(b, ans+"\n"), nil
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

// --- The motivating case: a name-squatting binary on PATH -------------------

// TestAdmitCompanions_UnconfirmedThirdPartyRefusedNonInteractively is the
// threat this whole file exists for: an npm dependency drops
// ctxloom-companion-* into ./node_modules/.bin, which is on PATH, and the next
// session start used to exec it with no user action at all.
func TestAdmitCompanions_UnconfirmedThirdPartyRefusedNonInteractively(t *testing.T) {
	f := newConsentFixture(t)
	f.writeBin(t, f.elsewhere, "ctxloom-companion-acme", "#!/bin/sh\n")

	got := admissionFor(t, AdmitCompanions([]string{"ctxloom-companion-acme"}, true), "ctxloom-companion-acme")

	assert.False(t, got.Allowed, "a companion nobody confirmed must never be exec'd")
	assert.Equal(t, CompanionAdmissionUnconfirmed, got.Reason)
	assert.Contains(t, f.warnLog.String(), "never confirmed for execution",
		"the refusal must be VISIBLE — a silently skipped companion is the silent no-op")
	assert.Contains(t, f.warnLog.String(), "trust companion allow",
		"the warning must name the way out, or the user is stuck")
	assert.Equal(t, 0, f.prompted, "a non-interactive session must never prompt")
}

// TestAdmitCompanions_NonInteractiveNeverPrompts pins the fail-closed half
// separately from the message: the prompt path must not be reached AT ALL, so
// an agent session can never block on a question nobody can see.
func TestAdmitCompanions_NonInteractiveNeverPrompts(t *testing.T) {
	f := newConsentFixture(t)
	f.writeBin(t, f.elsewhere, "ctxloom-companion-acme", "#!/bin/sh\n")

	AdmitCompanions([]string{"ctxloom-companion-acme"}, true)

	assert.Empty(t, f.promptLog.String(), "nothing may be written to the prompt channel in a non-interactive session")
}

// --- The prompt, and what it records ---------------------------------------

func TestAdmitCompanions_InteractiveYesRecordsPathAndHash(t *testing.T) {
	f := newConsentFixture(t)
	path := f.writeBin(t, f.elsewhere, "ctxloom-companion-acme", "#!/bin/sh\necho one\n")
	f.interactive(t, "y")

	got := admissionFor(t, AdmitCompanions([]string{"ctxloom-companion-acme"}, true), "ctxloom-companion-acme")
	require.True(t, got.Allowed)
	assert.Equal(t, CompanionAdmissionConsented, got.Reason)
	assert.Equal(t, 1, f.prompted)
	assert.Contains(t, f.promptLog.String(), "ctxloom EXECUTES this file",
		"the question must say what allowing actually grants")
	assert.Contains(t, f.promptLog.String(), path, "the question must name the absolute path, not just the name")

	records, err := ListCompanionConsent()
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, path, records[0].Path)
	assert.True(t, records[0].Approved)
	assert.Len(t, records[0].SHA256, 64, "the record must carry the binary's full sha256, not a path alone")

	// Second call: the record answers, nothing is asked again.
	f.prompted = 0
	again := admissionFor(t, AdmitCompanions([]string{"ctxloom-companion-acme"}, true), "ctxloom-companion-acme")
	assert.True(t, again.Allowed)
	assert.Equal(t, 0, f.prompted, "a recorded decision must not be re-asked")
}

func TestAdmitCompanions_InteractiveNoRecordsDenialAndWithholds(t *testing.T) {
	f := newConsentFixture(t)
	f.writeBin(t, f.elsewhere, "ctxloom-companion-acme", "#!/bin/sh\n")
	f.interactive(t, "n")

	got := admissionFor(t, AdmitCompanions([]string{"ctxloom-companion-acme"}, true), "ctxloom-companion-acme")
	assert.False(t, got.Allowed)
	assert.Equal(t, CompanionAdmissionDeclined, got.Reason)

	records, err := ListCompanionConsent()
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.False(t, records[0].Approved, "a decline must be RECORDED, or every session re-asks and trains reflex approval")
}

// TestAdmitCompanions_EmptyAnswerIsNo pins the default of a security question.
func TestAdmitCompanions_EmptyAnswerIsNo(t *testing.T) {
	f := newConsentFixture(t)
	f.writeBin(t, f.elsewhere, "ctxloom-companion-acme", "#!/bin/sh\n")
	f.interactive(t, "")

	got := admissionFor(t, AdmitCompanions([]string{"ctxloom-companion-acme"}, true), "ctxloom-companion-acme")
	assert.False(t, got.Allowed, "bare Enter must decline, never allow")
}

// --- The hash half of the key ----------------------------------------------

// TestAdmitCompanions_ApprovalIsBoundToTheBytes is the sub-decision the human
// REVERSED a path-only design for: an approved path whose binary is replaced
// re-prompts, because the approval covers bytes and not a location.
func TestAdmitCompanions_ApprovalIsBoundToTheBytes(t *testing.T) {
	f := newConsentFixture(t)
	path := f.writeBin(t, f.elsewhere, "ctxloom-companion-acme", "#!/bin/sh\noriginal\n")
	f.interactive(t, "y")
	require.True(t, admissionFor(t, AdmitCompanions([]string{"ctxloom-companion-acme"}, true), "ctxloom-companion-acme").Allowed)

	// Swap the file in place — the attacker who already had write access here.
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\nSWAPPED\n"), 0o755)) //nolint:gosec // fixture
	companionSessionInteractive = func() bool { return false }

	got := admissionFor(t, AdmitCompanions([]string{"ctxloom-companion-acme"}, true), "ctxloom-companion-acme")
	assert.False(t, got.Allowed, "the bytes changed at an approved path — the approval must not carry over")
	assert.Equal(t, CompanionAdmissionUnconfirmed, got.Reason)
}

// TestAdmitCompanions_DenialSurvivesARebuild is the ASYMMETRY: an approval is
// hash-bound, a refusal is not. "Never run this" that a rebuild clears would be
// no refusal at all.
func TestAdmitCompanions_DenialSurvivesARebuild(t *testing.T) {
	f := newConsentFixture(t)
	path := f.writeBin(t, f.elsewhere, "ctxloom-companion-acme", "#!/bin/sh\nv1\n")
	f.interactive(t, "n")
	require.False(t, admissionFor(t, AdmitCompanions([]string{"ctxloom-companion-acme"}, true), "ctxloom-companion-acme").Allowed)

	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\nv2\n"), 0o755)) //nolint:gosec // fixture
	f.prompted = 0
	f.answers = []string{"y"} // would say yes if asked

	got := admissionFor(t, AdmitCompanions([]string{"ctxloom-companion-acme"}, true), "ctxloom-companion-acme")
	assert.False(t, got.Allowed, "a recorded refusal must survive the binary being rebuilt")
	assert.Equal(t, CompanionAdmissionDeclined, got.Reason)
	assert.Equal(t, 0, f.prompted, "a refused path must not be re-asked into an approval")
}

// --- First-party: exempt, but PINNED BY LOCATION ---------------------------

// TestFirstPartyCompanion_ExemptOnlyFromTheInstallDirectory is the load-bearing
// half of the first-party exemption. Same NAME, two locations, opposite answers
// — without the location pin the exemption is just a list of three guessable
// names that anything earlier on $PATH can claim.
func TestFirstPartyCompanion_ExemptOnlyFromTheInstallDirectory(t *testing.T) {
	f := newConsentFixture(t)
	f.writeBin(t, f.installDir, "ltk", "#!/bin/sh\nreal ltk\n")

	got := admissionFor(t, AdmitCompanions([]string{"ltk"}, true), "ltk")
	assert.True(t, got.Allowed, "`just install` must stay silent: ltk next to ctxloom is automatic")
	assert.Equal(t, CompanionAdmissionFirstParty, got.Reason)
	assert.Equal(t, 0, f.prompted)
	assert.Empty(t, got.SHA256, "the first-party arm must not pay to hash a ~60MB binary on every startup")

	// The SAME name, somewhere else on PATH: a stranger that picked a familiar
	// name, and it gets no exemption at all.
	f2 := newConsentFixture(t)
	f2.writeBin(t, f2.elsewhere, "ltk", "#!/bin/sh\nimposter\n")

	shadow := admissionFor(t, AdmitCompanions([]string{"ltk"}, true), "ltk")
	assert.False(t, shadow.Allowed, "a first-party NAME outside the install directory is a third-party binary")
	assert.Equal(t, CompanionAdmissionUnconfirmed, shadow.Reason)
}

// TestFirstPartyCompanion_RebuildInPlaceStaysSilent pins the reason the
// exemption exists at all: this repo rebuilds ltk and taskloom constantly, and
// a prompt on every `just install` would train reflex approval.
func TestFirstPartyCompanion_RebuildInPlaceStaysSilent(t *testing.T) {
	f := newConsentFixture(t)
	path := f.writeBin(t, f.installDir, "taskloom", "#!/bin/sh\nbuild 1\n")
	require.True(t, admissionFor(t, AdmitCompanions([]string{"taskloom"}, true), "taskloom").Allowed)

	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\nbuild 2\n"), 0o755)) //nolint:gosec // fixture
	got := admissionFor(t, AdmitCompanions([]string{"taskloom"}, true), "taskloom")
	assert.True(t, got.Allowed)
	assert.Equal(t, 0, f.prompted, "rebuilding a first-party companion in place must not ask anything")
}

// TestFirstPartyCompanion_RecordedRefusalBeatsTheExemption: the exemption is an
// ALLOW rule, and a human's refusal outranks every allow — the same ordering
// rejection has in EffectiveTrust.
func TestFirstPartyCompanion_RecordedRefusalBeatsTheExemption(t *testing.T) {
	f := newConsentFixture(t)
	path := f.writeBin(t, f.installDir, "ltk", "#!/bin/sh\n")
	_, err := SetCompanionConsent(path, false)
	require.NoError(t, err)

	got := admissionFor(t, AdmitCompanions([]string{"ltk"}, true), "ltk")
	assert.False(t, got.Allowed, "a recorded refusal must reach even a first-party companion in the install directory")
	assert.Equal(t, CompanionAdmissionDeclined, got.Reason)
}

// TestFirstPartyCompanion_UnresolvableInstallDirGrantsNothing: if the location
// cannot be established, the exemption does not apply — it does not widen.
func TestFirstPartyCompanion_UnresolvableInstallDirGrantsNothing(t *testing.T) {
	f := newConsentFixture(t)
	f.writeBin(t, f.installDir, "ltk", "#!/bin/sh\n")
	companionInstallDir = func() (string, error) { return "", os.ErrNotExist }

	got := admissionFor(t, AdmitCompanions([]string{"ltk"}, true), "ltk")
	assert.False(t, got.Allowed, "an unknown install location must not be treated as 'everywhere'")
}

// --- The store's own fault path --------------------------------------------

// TestAdmitCompanions_UnreadableConsentRecordDeniesEverything mirrors
// EffectiveTrust's unreadable-approvals-store gate: a record we cannot read may
// hold a REFUSAL, so it denies every companion, exemptions included.
func TestAdmitCompanions_UnreadableConsentRecordDeniesEverything(t *testing.T) {
	f := newConsentFixture(t)
	f.writeBin(t, f.installDir, "ltk", "#!/bin/sh\n")
	f.writeBin(t, f.elsewhere, "ctxloom-companion-acme", "#!/bin/sh\n")
	require.NoError(t, os.WriteFile(f.recordPath, []byte("{{{ not yaml at all"), 0o600))

	got := AdmitCompanions([]string{"ltk", "ctxloom-companion-acme"}, true)
	for _, a := range got {
		assert.False(t, a.Allowed, "%s must be denied while the consent record is unreadable", a.Bin)
		assert.Equal(t, CompanionAdmissionStoreFault, a.Reason, a.Bin)
	}
	assert.Contains(t, f.warnLog.String(), "companion consent record unreadable")
}

// TestSetCompanionConsent_RefusesToOverwriteAnUnreadableRecord: a write that
// silently discarded decisions it could not read would be a data-loss bug
// wearing a success message.
func TestSetCompanionConsent_RefusesToOverwriteAnUnreadableRecord(t *testing.T) {
	f := newConsentFixture(t)
	path := f.writeBin(t, f.elsewhere, "ctxloom-companion-acme", "#!/bin/sh\n")
	require.NoError(t, os.WriteFile(f.recordPath, []byte("{{{ not yaml"), 0o600))

	_, err := SetCompanionConsent(path, true)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "refusing to overwrite")
}

// TestAdmitCompanions_UnknownRecordVersionIsAFaultNotAnEmptyStore: a format
// this build does not understand must not read as "nothing was decided".
func TestAdmitCompanions_UnknownRecordVersionIsAFaultNotAnEmptyStore(t *testing.T) {
	f := newConsentFixture(t)
	f.writeBin(t, f.installDir, "ltk", "#!/bin/sh\n")
	require.NoError(t, os.WriteFile(f.recordPath, []byte("version: 99\ncompanions: []\n"), 0o600))

	got := admissionFor(t, AdmitCompanions([]string{"ltk"}, true), "ltk")
	assert.False(t, got.Allowed)
	assert.Equal(t, CompanionAdmissionStoreFault, got.Reason)
}

// --- Identity resolution ---------------------------------------------------

// TestAdmitCompanions_ConsentKeysOnTheRESOLVEDPath proves a symlinked companion
// records the file it points AT, so swapping the link's target cannot inherit
// the approval granted to the old target.
func TestAdmitCompanions_ConsentKeysOnTheResolvedPath(t *testing.T) {
	f := newConsentFixture(t)
	realA := f.writeBin(t, t.TempDir(), "real-a", "#!/bin/sh\nA\n")
	realB := f.writeBin(t, t.TempDir(), "real-b", "#!/bin/sh\nB\n")
	link := filepath.Join(f.elsewhere, "ctxloom-companion-acme")
	require.NoError(t, os.Symlink(realA, link))

	f.interactive(t, "y")
	require.True(t, admissionFor(t, AdmitCompanions([]string{"ctxloom-companion-acme"}, true), "ctxloom-companion-acme").Allowed)
	records, err := ListCompanionConsent()
	require.NoError(t, err)
	require.Len(t, records, 1)
	assert.Equal(t, realA, records[0].Path, "the record must name the file that actually runs")

	require.NoError(t, os.Remove(link))
	require.NoError(t, os.Symlink(realB, link))
	companionSessionInteractive = func() bool { return false }

	got := admissionFor(t, AdmitCompanions([]string{"ctxloom-companion-acme"}, true), "ctxloom-companion-acme")
	assert.False(t, got.Allowed, "repointing the symlink is a different binary and must be asked about")
}

// TestAdmitCompanions_MissingBinaryIsSilentlyNotInstalled: most machines have
// no reprise, and that must not produce a warning.
func TestAdmitCompanions_MissingBinaryIsSilentlyNotInstalled(t *testing.T) {
	f := newConsentFixture(t)

	got := admissionFor(t, AdmitCompanions([]string{"reprise"}, true), "reprise")
	assert.False(t, got.Allowed)
	assert.Equal(t, CompanionAdmissionNotInstalled, got.Reason)
	assert.Empty(t, got.Path)
	assert.Empty(t, f.warnLog.String(), "a companion that simply is not installed is ordinary, not a warning")
}

// --- The user-facing record surface ----------------------------------------

func TestCompanionConsentCLISurface_AllowListForget(t *testing.T) {
	f := newConsentFixture(t)
	path := f.writeBin(t, f.elsewhere, "ctxloom-companion-acme", "#!/bin/sh\n")

	empty, err := ListCompanionConsent()
	require.NoError(t, err)
	assert.Empty(t, empty)

	rec, err := SetCompanionConsent("ctxloom-companion-acme", true) // by bare NAME, via PATH
	require.NoError(t, err)
	assert.Equal(t, path, rec.Path, "a bare name must resolve through PATH to the same key the probe uses")

	listed, err := ListCompanionConsent()
	require.NoError(t, err)
	require.Len(t, listed, 1)
	assert.True(t, listed[0].Approved)

	removed, err := ForgetCompanionConsent(path)
	require.NoError(t, err)
	assert.Equal(t, 1, removed)

	after, err := ListCompanionConsent()
	require.NoError(t, err)
	assert.Empty(t, after)

	// Forgetting something never recorded reports zero rather than pretending.
	removed, err = ForgetCompanionConsent(path)
	require.NoError(t, err)
	assert.Equal(t, 0, removed)
}

// TestCompanionConsentRecord_IsPersonalOnlyAndNotWorldReadable: the record's
// only authority is filesystem permissions, so those permissions ARE the
// security property.
func TestCompanionConsentRecord_IsPersonalOnlyAndNotWorldReadable(t *testing.T) {
	f := newConsentFixture(t)
	f.writeBin(t, f.elsewhere, "ctxloom-companion-acme", "#!/bin/sh\n")
	_, err := SetCompanionConsent("ctxloom-companion-acme", true)
	require.NoError(t, err)

	info, err := os.Stat(f.recordPath)
	require.NoError(t, err)
	assert.Equal(t, os.FileMode(0o600), info.Mode().Perm(),
		"a record anything on the machine could rewrite would decide execution for the user")

	home, herr := os.UserHomeDir()
	require.NoError(t, herr)
	real, perr := paths.HomeCompanionConsentPath()
	require.NoError(t, perr)
	assert.True(t, strings.HasPrefix(real, home),
		"the consent record lives under HOME and has no committable project twin: %s", real)
}

// TestCompanionConsent_ReDecidingReplacesRatherThanAccumulates: a path holds
// exactly one live decision, so an older approval for a superseded build can
// never sit behind a newer refusal.
func TestCompanionConsent_ReDecidingReplacesRatherThanAccumulates(t *testing.T) {
	f := newConsentFixture(t)
	path := f.writeBin(t, f.elsewhere, "ctxloom-companion-acme", "#!/bin/sh\nv1\n")
	_, err := SetCompanionConsent(path, true)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(path, []byte("#!/bin/sh\nv2\n"), 0o755)) //nolint:gosec // fixture
	_, err = SetCompanionConsent(path, false)
	require.NoError(t, err)

	records, err := ListCompanionConsent()
	require.NoError(t, err)
	require.Len(t, records, 1, "one path, one live decision")
	assert.False(t, records[0].Approved)
}

// --- The probes actually honour the gate -----------------------------------

// TestProbeCompanionLoadouts_NeverExecsAnUnadmittedCompanion is the assertion
// that matters most: not "the map came back empty" (a broken probe produces
// that too) but "the binary was never run". The exec seam is the witness.
func TestProbeCompanionLoadouts_NeverExecsAnUnadmittedCompanion(t *testing.T) {
	f := newConsentFixture(t)
	f.writeBin(t, f.elsewhere, "ctxloom-companion-acme", "#!/bin/sh\n")
	restorePath := setPathDirsForTesting(t, []string{f.elsewhere, f.installDir})
	defer restorePath()

	var execed []string
	restoreProbe := SetCompanionLoadoutOutputForTesting(func(path string) ([]byte, error) {
		execed = append(execed, path)
		return nil, os.ErrNotExist
	})
	defer restoreProbe()

	got, err := ProbeCompanionLoadouts(context.Background())
	require.NoError(t, err)
	assert.Empty(t, got)
	assert.Empty(t, execed, "an unconfirmed companion must never be exec'd, not merely have its output discarded")

	// Now grant consent and prove the SAME fixture does run — otherwise the
	// assertion above would also pass against a probe that is simply broken.
	_, err = SetCompanionConsent("ctxloom-companion-acme", true)
	require.NoError(t, err)
	_, _ = ProbeCompanionLoadouts(context.Background())
	assert.Len(t, execed, 1, "with consent recorded the very same companion is exec'd")
}

// TestProbeCompanions_ReportsRefusalRatherThanAbsence: a refused companion is
// present on the machine, and reporting it as "not installed" would send the
// user chasing an install that already happened.
func TestProbeCompanions_ReportsRefusalRatherThanAbsence(t *testing.T) {
	f := newConsentFixture(t)
	path := f.writeBin(t, f.elsewhere, "ctxloom-companion-acme", "#!/bin/sh\n")
	restorePath := setPathDirsForTesting(t, []string{f.elsewhere, f.installDir})
	defer restorePath()

	var execed []string
	restoreVersion := SetCompanionVersionOutputForTesting(func(p string) ([]byte, error) {
		execed = append(execed, p)
		return []byte(`{"version":"1.0.0"}`), nil
	})
	defer restoreVersion()

	var acme CompanionStatus
	for _, st := range ProbeCompanions() {
		if st.Bin == "ctxloom-companion-acme" {
			acme = st
		}
	}
	require.Equal(t, "ctxloom-companion-acme", acme.Bin, "the refused companion must still be REPORTED")
	assert.Equal(t, path, acme.Path, "the report must name the file it refused, not pretend nothing is there")
	assert.Equal(t, CompanionAdmissionUnconfirmed, acme.Admission)
	assert.False(t, acme.Executed())
	assert.Empty(t, execed, "the version probe is an exec too, and must not run without consent")
}

// TestAdmitCompanions_PromptFalseNeverAsksEvenInteractively pins the
// reporting-caller contract: looking at companion state must never conjure a
// security question.
func TestAdmitCompanions_PromptFalseNeverAsksEvenInteractively(t *testing.T) {
	f := newConsentFixture(t)
	f.writeBin(t, f.elsewhere, "ctxloom-companion-acme", "#!/bin/sh\n")
	f.interactive(t, "y")

	got := admissionFor(t, AdmitCompanions([]string{"ctxloom-companion-acme"}, false), "ctxloom-companion-acme")
	assert.False(t, got.Allowed)
	assert.Equal(t, 0, f.prompted, "a caller that passes prompt=false must never ask, even on a terminal")
}
