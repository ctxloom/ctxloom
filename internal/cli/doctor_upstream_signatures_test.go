package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/remote"
)

// --- DOCTOR-CHECK-UPSTREAM-SIGNATURES-o5 ------------------------------------

// upstreamRefusalProject scaffolds the minimum doctor's advisory reads: a
// lockfile pinning ref at kept, and a refusal record saying an advance to
// proposed was declined. Written as literal bytes rather than through the
// writer's own API, so the check is proven to read the format that is actually
// on disk.
func upstreamRefusalProject(t *testing.T, ref, kept, proposed string) *config.Config {
	t.Helper()
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0o755))

	lock := &remote.Lockfile{Version: 1, Bundles: map[string]remote.LockEntry{}}
	lock.AddEntry(remote.ItemTypeBundle, ref, remote.LockEntry{SHA: kept, URL: "file:///team"})
	require.NoError(t, remote.NewLockfileManager(appDir).Save(lock))

	recPath := paths.RefusedAdvancesPath(appDir)
	require.NoError(t, os.MkdirAll(filepath.Dir(recPath), 0o755))
	body := fmt.Sprintf("version: 1\nrefusals:\n  - identity: %q\n    kept_sha: %q\n    proposed_sha: %q\n"+
		"    detail: \"the signature does not cover these bytes\"\n    refused_at: 2026-08-05T10:32:00Z\n", ref, kept, proposed)
	require.NoError(t, os.WriteFile(recPath, []byte(body), 0o644))

	return config.NewFixture(config.Fixture{AppPaths: []string{appDir}})
}

const (
	upstreamRef      = "file:///team@bundles/deploy-runbook"
	upstreamKept     = "1111111111111111111111111111111111111111"
	upstreamProposed = "2222222222222222222222222222222222222222"
)

// The advisory has to carry the three facts a person cannot infer: WHICH
// bundle, WHICH revision was refused, and WHICH pin they are being kept at.
// Names alone would leave "which of the two SHAs am I on?" unanswered.
func TestDoctorCheckUpstreamSignatures_NamesTheBundleTheRefusedRevisionAndTheKeptPin(t *testing.T) {
	cfg := upstreamRefusalProject(t, upstreamRef, upstreamKept, upstreamProposed)
	c := doctorCheckUpstreamSignatures(cfg, nil)

	assert.Equal(t, "DOCTOR-CHECK-UPSTREAM-SIGNATURES-o5", c.Marker)
	assert.Equal(t, doctorWarn, c.Status, "something needs fixing — upstream — so this is not an [info]")
	assert.Contains(t, c.Detail, "deploy-runbook", "the bundle")
	assert.Contains(t, c.Detail, shortSHA(upstreamProposed), "the revision that was refused")
	assert.Contains(t, c.Detail, shortSHA(upstreamKept), "the pin being kept")
	assert.Contains(t, c.Detail, "2026-08-05", "an as-of advisory has to say as of when")
}

// THE FRAMING IS THE PAYLOAD. This advisory reports something the reader
// CANNOT fix locally, and a message that reads as a local misconfiguration
// sends them editing a trust store to fix a problem that is not on their
// machine. It must place the fault on the publisher, say the machine is fine,
// and name re-signing — not any local remedy.
func TestDoctorCheckUpstreamSignatures_BlamesThePublisherNotTheUsersMachine(t *testing.T) {
	cfg := upstreamRefusalProject(t, upstreamRef, upstreamKept, upstreamProposed)
	c := doctorCheckUpstreamSignatures(cfg, nil)

	assert.Contains(t, c.Detail, "publisher", "the fault is upstream and the message must say whose it is")
	assert.Contains(t, c.Detail, "re-sign", "the only real remedy")
	assert.Contains(t, c.Detail, "Nothing is wrong on this machine",
		"an advisory that reads as a local fault is worse than none: it sends people to fix what is not broken")
	// The local remedies of the check NEXT DOOR (n4, unsigned content) are
	// exactly the wrong advice here and must not leak into this message.
	assert.NotContains(t, c.Detail, "trust signer create")
	assert.NotContains(t, c.Detail, "ctxloom review")
}

// A record that no longer describes the pin the lockfile holds is a claim
// about a world that has moved on. Reporting it would be the same disease one
// level over — and worse than silence, because a doctor that cries wolf is one
// people stop reading.
func TestDoctorCheckUpstreamSignatures_IsSilentWhenTheKeptPinHasMovedOn(t *testing.T) {
	cfg := upstreamRefusalProject(t, upstreamRef, upstreamKept, upstreamProposed)
	appDir := cfg.GetAppPaths()[0]

	mgr := remote.NewLockfileManager(appDir)
	lock, err := mgr.Load()
	require.NoError(t, err)
	entry, ok := lock.GetEntry(remote.ItemTypeBundle, upstreamRef)
	require.True(t, ok)
	entry.SHA = "3333333333333333333333333333333333333333"
	lock.AddEntry(remote.ItemTypeBundle, upstreamRef, entry)
	require.NoError(t, mgr.Save(lock))

	c := doctorCheckUpstreamSignatures(cfg, nil)
	assert.Equal(t, doctorOK, c.Status, "a stale record must not be reported as a live problem")
	assert.NotContains(t, c.Detail, "deploy-runbook")
}

// The healthy project, and the reason this check is not simply always quiet:
// it says which question it answered, so a reader can tell "checked, nothing
// refused" from "this check does not exist".
func TestDoctorCheckUpstreamSignatures_RightState_NothingRefused(t *testing.T) {
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	require.NoError(t, os.MkdirAll(appDir, 0o755))
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})

	c := doctorCheckUpstreamSignatures(cfg, nil)
	assert.Equal(t, doctorOK, c.Status)
	assert.Contains(t, c.Detail, "no upstream revision has been refused")
}

// "I could not check" is not "it checks out". An unreadable record must warn,
// never print the clean bill of health an empty one would.
func TestDoctorCheckUpstreamSignatures_WrongState_UnreadableRecordWarns(t *testing.T) {
	appDir := filepath.Join(t.TempDir(), ".ctxloom")
	recPath := paths.RefusedAdvancesPath(appDir)
	require.NoError(t, os.MkdirAll(filepath.Dir(recPath), 0o755))
	require.NoError(t, os.WriteFile(recPath, []byte("version: 999\n"), 0o644))
	cfg := config.NewFixture(config.Fixture{AppPaths: []string{appDir}})

	c := doctorCheckUpstreamSignatures(cfg, nil)
	assert.Equal(t, doctorWarn, c.Status)
	assert.Contains(t, c.Detail, "could not read the record of refused upgrades")
	assert.NotContains(t, c.Detail, "no upstream revision has been refused")
}

// The check has to be IN the report. A check nobody runs is a function with
// tests, not a surface — and this whole slice exists because the fact had no
// surface at all.
func TestDoctorCmd_RendersTheUpstreamSignaturesCheck(t *testing.T) {
	root, _ := setupProject(t, "claude-code")
	out, err := runDoctor(t, root)
	require.NoError(t, err)
	assert.Contains(t, out, "DOCTOR-CHECK-UPSTREAM-SIGNATURES-o5",
		"`ctxloom doctor` must actually render the check; one omitted from runDoctorCmd's list reports nothing to anyone")
}

// --deps is the pre-setup mode (init's PRIME, the setup skill's phase 1): only
// machine-capability probes. This advisory is about a project's lockfile and a
// publisher's signatures, neither of which exists yet in that mode, so it must
// stay out of it.
func TestDoctorCmd_UpstreamSignaturesCheckIsNotADepsProbe(t *testing.T) {
	root, _ := setupProject(t, "claude-code")
	out, err := runDoctor(t, root, "--deps")
	require.NoError(t, err)
	assert.NotContains(t, out, "DOCTOR-CHECK-UPSTREAM-SIGNATURES-o5")
}
