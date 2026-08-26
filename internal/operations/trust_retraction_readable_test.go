package operations

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/spf13/afero"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// This is the READ side of the corrupt-lockfile defect. A previous fix
// stopped a corrupt lock.yaml being OVERWRITTEN; it says nothing about what
// EffectiveTrust does when it cannot READ one. That path failed OPEN:
// buildLockfileRetraction degraded an unparseable lockfile to "nothing is
// retracted", so content the publisher deliberately WITHDREW was served to
// the agent again. Retraction exists for "this turned out to be harmful";
// failing open on it inverts the control.
//
// The decided posture is FAIL CLOSED, expressed through the machinery the
// corrupt-APPROVALS-store path already uses (strictness.ClassTrust +
// trust.Deny) — see trust_approvals_readable_test.go, whose four cases these
// deliberately mirror one-for-one.
//
// Everything filesystem-shaped here uses afero.NewOsFs + t.TempDir: the
// defect is a real YAML parse failure over real bytes, and a MemMapFs fake
// would let a wrong implementation pass.

// corruptLockYAML is a truncated/half-written lock.yaml — valid-looking but
// unparseable (an unterminated quoted scalar), byte-identical in shape to the
// fixture the WRITE-side guard's own test uses
// (TestLockDependencies_CorruptLockfileIsNotOverwritten).
const corruptLockYAML = "version: 1\nbundles:\n  https://github.com/acme/repo@bundles/tooling:\n    sha: \"abc\n    pinned: true\n"

// writeLockYAML drops raw bytes at the lockfile path a config rooted at
// baseDir resolves to, creating the directory. Raw bytes rather than
// LockfileManager.Save on purpose: these tests are about what the PARSE does,
// so they must exercise the real parse over real content.
func writeLockYAML(t *testing.T, baseDir, content string) string {
	t.Helper()
	path := remote.NewLockfileManager(baseDir).Path()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	return path
}

// retractionTestRef is a remote bundle ref whose lockfile key is
// "https://github.com/acme/repo@bundles/tooling" (see lockfileKeyForRef).
func retractionTestRef() trust.Ref {
	return trust.Ref{RepoURL: trustRepo, Bundle: "tooling", Kind: trust.KindFragment, Name: "helper"}
}

// TestEffectiveTrust_CorruptLockfile_WithholdsRemoteContent is the HEADLINE
// case. A remote item that would otherwise be ALLOWED — here by step 5, a
// verified signature from a trusted publisher key — must be WITHHELD once the
// lockfile proves unreadable, because the retraction that would have beaten
// that signature is exactly what we can no longer read. Proving it on an
// otherwise-allowed item is the point: a pending item is denied either way,
// so it could not distinguish fail-open from fail-closed.
func TestEffectiveTrust_CorruptLockfile_WithholdsRemoteContent(t *testing.T) {
	resetStrictness(t)
	t.Setenv("HOME", t.TempDir())
	fs := afero.NewOsFs()
	baseDir := t.TempDir()
	writeLockYAML(t, baseDir, corruptLockYAML)
	cfg := testConfigWithSCMPath(baseDir)

	mark := strictness.Checkpoint()
	res, err := EffectiveTrust(cfg, EffectiveTrustRequest{
		Ref:        retractionTestRef(),
		Posture:    postureCtxOf(retractionTestRef()),
		Provenance: postureProvOf(retractionTestRef()),
		Payload:    pbytes("x"),
		Form:       rawForm,
		Signer:     "publisher@example.com",
		FS:         fs,
	})
	require.NoError(t, err)
	assert.Equal(t, trust.Deny, res.Decision,
		"an unreadable lockfile must withhold remote content: the retraction that would beat this signature is precisely what cannot be read")
	assert.NotEqual(t, trust.SourceTrustedSigner, res.Source,
		"the trusted-signer allow must never be reached once retraction state proves unreadable — a publisher can retract content signed by a key you still trust")

	found := strictness.Since(mark)
	require.Len(t, found, 1, "an unreadable lockfile must record exactly one fatal finding")
	assert.Equal(t, strictness.ClassTrust, found[0].Class,
		"unreadable trust state is ClassTrust, the same class the corrupt approvals store uses")
}

// TestEffectiveTrust_AbsentLockfile_NormalDecision is the BOUNDARY case that
// must be green THROUGHOUT this change: a project with no pinned dependencies
// has no lock.yaml at all, and legitimately has nothing retracted. Conflating
// "absent" with "unreadable" would turn every un-locked project into an
// abort — the same trap the approvals-store gate had to avoid
// (LockfileManager.Load already degrades os.IsNotExist to an empty lockfile,
// nil error; this pins that it stays that way).
func TestEffectiveTrust_AbsentLockfile_NormalDecision(t *testing.T) {
	resetStrictness(t)
	t.Setenv("HOME", t.TempDir())
	fs := afero.NewOsFs()
	baseDir := t.TempDir() // deliberately never given a lock.yaml
	cfg := testConfigWithSCMPath(baseDir)

	mark := strictness.Checkpoint()

	// A signed remote item resolves its ordinary ALLOW.
	res, err := EffectiveTrust(cfg, EffectiveTrustRequest{
		Ref:        retractionTestRef(),
		Posture:    postureCtxOf(retractionTestRef()),
		Provenance: postureProvOf(retractionTestRef()),
		Payload:    pbytes("x"),
		Form:       rawForm,
		Signer:     "publisher@example.com",
		FS:         fs,
	})
	require.NoError(t, err)
	assert.Equal(t, trust.Allow, res.Decision, "an absent lockfile is not corruption — a project with no pins must keep working")
	assert.Equal(t, trust.SourceTrustedSigner, res.Source)

	// An unsigned remote item resolves the everyday "awaiting review" pending,
	// not a fail-closed deny.
	res2, err2 := EffectiveTrust(cfg, EffectiveTrustRequest{
		Ref:        trust.Ref{RepoURL: trustRepo, Bundle: "tooling", Kind: trust.KindFragment, Name: "never-reviewed"},
		Posture:    postureCtxOf(trust.Ref{RepoURL: trustRepo, Bundle: "tooling", Kind: trust.KindFragment, Name: "never-reviewed"}),
		Provenance: postureProvOf(trust.Ref{RepoURL: trustRepo, Bundle: "tooling", Kind: trust.KindFragment, Name: "never-reviewed"}),
		Payload:    pbytes("y"),
		Form:       rawForm,
		FS:         fs,
	})
	require.NoError(t, err2)
	assert.Equal(t, trust.Deny, res2.Decision)
	assert.Equal(t, trust.SourcePending, res2.Source, "an absent lockfile must resolve ordinary pending, never a fail-closed deny")

	assert.Empty(t, strictness.Since(mark), "an absent lockfile must never record a strictness finding")
}

// TestEffectiveTrust_ValidLockfile_RetractionBehaviorUnchanged is the
// NO-REGRESSION case: a lockfile that parses fine behaves exactly as it does
// today — the recorded retraction denies with SourceRetracted and carries the
// publisher's stated reason, and a bundle with no retraction entry is
// untouched by the new gate.
func TestEffectiveTrust_ValidLockfile_RetractionBehaviorUnchanged(t *testing.T) {
	resetStrictness(t)
	t.Setenv("HOME", t.TempDir())
	fs := afero.NewOsFs()
	baseDir := t.TempDir()
	writeLockYAML(t, baseDir, ""+
		"version: 1\n"+
		"bundles:\n"+
		"  https://github.com/acme/repo@bundles/tooling:\n"+
		"    sha: abc123\n"+
		"    retracted: true\n"+
		"    retracted_reason: leaked a credential\n"+
		"  https://github.com/acme/repo@bundles/fine:\n"+
		"    sha: def456\n")
	cfg := testConfigWithSCMPath(baseDir)

	mark := strictness.Checkpoint()

	// The retracted bundle: denied, with the publisher's reason, beating an
	// otherwise-winning trusted signature.
	res, err := EffectiveTrust(cfg, EffectiveTrustRequest{
		Ref:        retractionTestRef(),
		Posture:    postureCtxOf(retractionTestRef()),
		Provenance: postureProvOf(retractionTestRef()),
		Payload:    pbytes("x"),
		Form:       rawForm,
		Signer:     "publisher@example.com",
		FS:         fs,
	})
	require.NoError(t, err)
	assert.Equal(t, trust.Deny, res.Decision)
	assert.Equal(t, trust.SourceRetracted, res.Source)
	assert.Equal(t, "leaked a credential", res.Detail, "the publisher's stated reason still rides through as display-only Detail")

	// A sibling bundle with no retraction recorded is unaffected.
	res2, err2 := EffectiveTrust(cfg, EffectiveTrustRequest{
		Ref:        trust.Ref{RepoURL: trustRepo, Bundle: "fine", Kind: trust.KindFragment, Name: "helper"},
		Posture:    postureCtxOf(trust.Ref{RepoURL: trustRepo, Bundle: "fine", Kind: trust.KindFragment, Name: "helper"}),
		Provenance: postureProvOf(trust.Ref{RepoURL: trustRepo, Bundle: "fine", Kind: trust.KindFragment, Name: "helper"}),
		Payload:    pbytes("y"),
		Form:       rawForm,
		Signer:     "publisher@example.com",
		FS:         fs,
	})
	require.NoError(t, err2)
	assert.Equal(t, trust.Allow, res2.Decision, "a readable lockfile with no retraction for this bundle allows exactly as before")
	assert.Equal(t, trust.SourceTrustedSigner, res2.Source)

	assert.Empty(t, strictness.Since(mark), "a readable lockfile must never record a strictness finding")
}

// TestEffectiveTrust_CorruptLockfile_LocalAndBuiltinStillAllowed pins the
// SCOPE of the deny. Unlike the approvals store — which holds REJECTIONS,
// and a local fragment can be rejected — the lockfile records only REMOTE
// bundle entries. A local or builtin ref has no lockfile entry by
// construction (lockfileRetraction.Retracted exempts them outright), so an
// unreadable lockfile conceals NOTHING about them. Withholding them would be
// withholding on the basis of state that provably could not exist, and would
// break `ctxloom run` in a project whose content is entirely first-party.
func TestEffectiveTrust_CorruptLockfile_LocalAndBuiltinStillAllowed(t *testing.T) {
	resetStrictness(t)
	t.Setenv("HOME", t.TempDir())
	fs := afero.NewOsFs()
	baseDir := t.TempDir()
	writeLockYAML(t, baseDir, corruptLockYAML)
	cfg := testConfigWithSCMPath(baseDir)

	local, err := EffectiveTrust(cfg, EffectiveTrustRequest{
		Ref:        trust.Ref{Bundle: "dev", Kind: trust.KindFragment, Name: "notes", IsLocal: true},
		Posture:    postureCtxOf(trust.Ref{Bundle: "dev", Kind: trust.KindFragment, Name: "notes", IsLocal: true}),
		Provenance: postureProvOf(trust.Ref{Bundle: "dev", Kind: trust.KindFragment, Name: "notes", IsLocal: true}),
		Payload:    pbytes("x"),
		Form:       rawForm,
		FS:         fs,
	})
	require.NoError(t, err)
	assert.Equal(t, trust.Allow, local.Decision, "a local ref can never carry a lockfile retraction, so an unreadable lockfile must not withhold it")
	assert.Equal(t, trust.SourceLocal, local.Source)

	builtin, err := EffectiveTrust(cfg, EffectiveTrustRequest{
		Ref:        trust.Ref{Bundle: "kit", Kind: trust.KindFragment, Name: "shipped", IsBuiltin: true},
		Posture:    postureCtxOf(trust.Ref{Bundle: "kit", Kind: trust.KindFragment, Name: "shipped", IsBuiltin: true}),
		Provenance: postureProvOf(trust.Ref{Bundle: "kit", Kind: trust.KindFragment, Name: "shipped", IsBuiltin: true}),
		Payload:    pbytes("y"),
		Form:       rawForm,
		FS:         fs,
	})
	require.NoError(t, err)
	assert.Equal(t, trust.Allow, builtin.Decision, "a builtin ref can never carry a lockfile retraction either")
	assert.Equal(t, trust.SourceBuiltin, builtin.Source)
}

// TestEffectiveTrust_CorruptLockfile_RejectionStillOutranks keeps rejection
// SUPREME. The new gate belongs to step 2 (retraction), so it must sit BELOW
// step 1: when a rejected item is evaluated against an unreadable lockfile,
// the user is told "rejected" — the truthful, more specific answer — rather
// than the generic fail-closed one. Both deny; only the reported Source
// differs, and it is the Source the user acts on.
func TestEffectiveTrust_CorruptLockfile_RejectionStillOutranks(t *testing.T) {
	resetStrictness(t)
	t.Setenv("HOME", t.TempDir())
	fs := afero.NewOsFs()
	baseDir := t.TempDir()
	writeLockYAML(t, baseDir, corruptLockYAML)
	cfg := testConfigWithSCMPath(baseDir)

	ref := retractionTestRef()
	res, err := EffectiveTrust(cfg, EffectiveTrustRequest{
		Ref:        ref,
		Posture:    postureCtxOf(ref),
		Provenance: postureProvOf(ref),
		Payload:    pbytes("x"),
		Form:       rawForm,
		Records:    fakeRecords{rejected: func(r trust.Ref, _ []byte) bool { return r.Bundle == "tooling" }},
		FS:         fs,
	})
	require.NoError(t, err)
	assert.Equal(t, trust.Deny, res.Decision)
	assert.Equal(t, trust.SourceRejected, res.Source, "rejection is supreme — the unreadable-retraction gate must not short-circuit ahead of it")
}

// TestEffectiveTrust_CorruptLockfile_FindingNamesTheRecovery is the message
// discipline. A user cannot otherwise diagnose this: nothing they typed
// mentions lock.yaml, and the abort has to read as a POLICY refusal, not a
// crash. Following internal/acp/fsconfine.go's discipline, it must name the
// reason, name the file, say the state is intact (the corrupt file is
// preserved byte-identical), and name the recovery.
func TestEffectiveTrust_CorruptLockfile_FindingNamesTheRecovery(t *testing.T) {
	resetStrictness(t)
	t.Setenv("HOME", t.TempDir())
	fs := afero.NewOsFs()
	baseDir := t.TempDir()
	lockPath := writeLockYAML(t, baseDir, corruptLockYAML)
	cfg := testConfigWithSCMPath(baseDir)

	mark := strictness.Checkpoint()
	_, err := EffectiveTrust(cfg, EffectiveTrustRequest{
		Ref: retractionTestRef(), Payload: pbytes("x"), Form: rawForm, Signer: "publisher@example.com", FS: fs,
		Posture: postureCtxOf(retractionTestRef()), Provenance: postureProvOf(retractionTestRef()),
	})
	require.NoError(t, err)

	found := strictness.Since(mark)
	require.Len(t, found, 1)

	assert.Contains(t, found[0].Message, lockPath, "the message names the exact file to fix")
	assert.Contains(t, found[0].Message, "retraction", "the message names the reason: retraction state cannot be established")
	assert.Contains(t, found[0].Message, "withholding", "the message says what it did, in policy terms")

	assert.Contains(t, found[0].FixIt, "delete", "the fix-it names the recovery: delete the file")
	assert.Contains(t, found[0].FixIt, "ctxloom remote lock", "the fix-it names the command that rebuilds it")
	assert.Contains(t, found[0].FixIt, "intact", "the fix-it reassures that the holds/retractions on disk were preserved, not destroyed")
}

// TestContentGate_CorruptLockfile_WithholdsRemoteContent is the
// PRODUCTION-SHAPED case, and it exists because the corrupt-approvals-store
// fix shipped a guard that turned out to be DEAD CODE
// on the real call path: every production caller injected its Records, so the
// gate — reachable only when Records was nil — never ran. Retraction has the
// mirror-image shape: buildContentGate never sets contentGate.retraction
// (nil is the production default), so buildLockfileRetraction runs per call
// and the step-2a gate IS reached. This pins that, through the real
// contentGate.allow entry point, so the guard can never quietly become
// unreachable the same way.
func TestContentGate_CorruptLockfile_WithholdsRemoteContent(t *testing.T) {
	resetStrictness(t)
	t.Setenv("HOME", t.TempDir())
	baseDir := t.TempDir()
	writeLockYAML(t, baseDir, corruptLockYAML)
	cfg := testConfigWithSCMPath(baseDir)

	// retraction left nil — exactly what buildContentGate produces.
	g := &contentGate{cfg: cfg, records: newTrustFixture(t).records(), fs: afero.NewOsFs()}

	assert.False(t, admitExec(t, g, execRead(t, "publisher@example.com"), solidDecideRef, pbytes("x"), rawForm),
		"the real gate entry point must withhold remote content when retraction state cannot be established")

	items := g.withheldItems()
	require.Len(t, items, 1, "the gate records the withheld ref, so the advisory can say WHY it was withheld")
	assert.Equal(t, solidDecideRef, items[0].Ref)
	assert.Equal(t, bundles.ReasonPending, items[0].Verdict.Reason)
}

// TestEffectiveTrust_CorruptLockfile_DegradedModeWarnsAndContinues pins the
// documented escape hatch: `--degraded` (CTXLOOM_DEGRADED=1) makes no
// finding, so no choke owner aborts. The DENY is not relaxed — degraded mode
// governs whether a fault is FATAL, never whether unreadable trust state may
// be treated as trustworthy.
func TestEffectiveTrust_CorruptLockfile_DegradedModeWarnsAndContinues(t *testing.T) {
	resetStrictness(t)
	strictness.SetDegraded(true)
	t.Cleanup(func() { strictness.SetDegraded(false) })
	t.Setenv("HOME", t.TempDir())
	fs := afero.NewOsFs()
	baseDir := t.TempDir()
	writeLockYAML(t, baseDir, corruptLockYAML)
	cfg := testConfigWithSCMPath(baseDir)

	mark := strictness.Checkpoint()
	res, err := EffectiveTrust(cfg, EffectiveTrustRequest{
		Ref: retractionTestRef(), Payload: pbytes("x"), Form: rawForm, Signer: "publisher@example.com", FS: fs,
		Posture: postureCtxOf(retractionTestRef()), Provenance: postureProvOf(retractionTestRef()),
	})
	require.NoError(t, err)
	assert.Equal(t, trust.Deny, res.Decision, "degraded mode governs FATALITY, never whether unreadable trust state may be trusted")
	assert.Empty(t, strictness.Actionable(strictness.Since(mark)),
		"degraded mode still COLLECTS the finding; what must be empty is what the gate acts on")
}
