package operations

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"golang.org/x/crypto/ssh"
	"gopkg.in/yaml.v3"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/paths"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/signing"
)

// refusal is one fixture's worth of the B2 cause, already run: a project
// pinned to a commit whose signature verifies, a publisher edit pushed WITHOUT
// a re-sign, and one upgrade round over it.
type refusal struct {
	cfg      *config.Config
	baseDir  string
	src      string
	ref      string
	signer   ssh.Signer
	kept     string
	proposed string
}

func newRefusal(t *testing.T) refusal {
	t.Helper()
	r := refusal{}
	r.baseDir, r.src, r.ref, r.signer, r.kept = signedBundleRepo(t, "name: demo\n")
	r.cfg = testConfigWithSCMPath(r.baseDir)

	ctx := context.Background()
	_, err := LockDependencies(ctx, r.cfg, LockDependenciesRequest{SkipSync: true, FailOnConflict: true})
	require.NoError(t, err)

	r.proposed = addFileToLocalRepo(t, r.src, ".ctxloom/content/bundles/demo.yaml", "version: \"2.0.0\"\n")
	require.NotEqual(t, r.kept, r.proposed)

	res, err := UpgradeDependencies(ctx, r.cfg)
	require.NoError(t, err)
	require.Len(t, res.Refused, 1, "the fixture must actually reach the refusal, or every assertion below is vacuous")
	return r
}

// The refusal has to OUTLIVE the sync that produced it. Before this record
// existed, `deps upgrade` was the only place the fact was ever stated: close
// the terminal and nothing on the machine knew a revision had been refused.
func TestRefusals_UpgradeRecordsTheRefusalWhereAnInspectorCanReadIt(t *testing.T) {
	r := newRefusal(t)

	// The file itself, read as bytes — not through the writer's own reader,
	// which could agree with itself about a file that was never written.
	raw, err := os.ReadFile(paths.RefusedAdvancesPath(r.baseDir))
	require.NoError(t, err, "the refusal must be persisted; without it `doctor` has nothing to report")
	var doc refusalDoc
	require.NoError(t, yaml.Unmarshal(raw, &doc))
	assert.Equal(t, refusalStoreVersion, doc.Version)
	require.Len(t, doc.Refusals, 1)
	assert.Equal(t, r.ref, doc.Refusals[0].Identity, "the record must name WHICH bundle")
	assert.Equal(t, r.proposed, doc.Refusals[0].ProposedSHA, "the record must name the REVISION that was refused")
	assert.Equal(t, r.kept, doc.Refusals[0].KeptSHA, "the record must name the pin being kept")
	assert.Contains(t, doc.Refusals[0].Detail, "signature does not cover these bytes")
	assert.False(t, doc.Refusals[0].RefusedAt.IsZero(), "an as-of advisory with no as-of is not one")

	live, err := LiveRefusedAdvances(r.cfg)
	require.NoError(t, err)
	require.Len(t, live, 1)
	assert.Equal(t, r.ref, live[0].Identity)
	assert.Equal(t, r.proposed, live[0].ProposedSHA)
	assert.Equal(t, r.kept, live[0].KeptSHA)
}

// CLEARING, MECHANISM 1: the publisher re-signs, the next upgrade advances,
// and the record goes. Nobody has to remember to clear it, and there is no
// clear verb to forget — which is what keeps the advisory from outliving the
// problem and training people to ignore doctor.
func TestRefusals_ASuccessfulAdvanceClearsTheRecord(t *testing.T) {
	r := newRefusal(t)
	require.FileExists(t, paths.RefusedAdvancesPath(r.baseDir))

	// Carol finally re-signs and republishes.
	const bundlePath = ".ctxloom/content/bundles/demo.yaml"
	body := "version: \"3.0.0\"\n"
	addFileToLocalRepo(t, r.src, bundlePath, body)
	sig, err := signing.Sign([]byte(body), r.signer, signing.NamespacePublish)
	require.NoError(t, err)
	addFileToLocalRepo(t, r.src, bundlePath+".sig", string(sig))

	res, err := UpgradeDependencies(context.Background(), r.cfg)
	require.NoError(t, err)
	require.Equal(t, 1, res.Advanced)
	require.Empty(t, res.Refused)

	assert.NoFileExists(t, paths.RefusedAdvancesPath(r.baseDir),
		"a round that refused nothing must DELETE the record; leaving it is how doctor starts reporting a problem that is already fixed")
	live, err := LiveRefusedAdvances(r.cfg)
	require.NoError(t, err)
	assert.Empty(t, live)
}

// CLEARING, MECHANISM 2: the pin moved by a path that runs no upgrade round.
// The record still claims "the pin for X is being KEPT at <sha>"; the lockfile
// says otherwise, so the claim is false and must not be reported. Without this
// read-time check a hand-edited lock, a `remote lock` rebuild or a re-pull
// would each leave doctor asserting something untrue.
func TestRefusals_ARecordWhoseKeptPinMovedIsNotReported(t *testing.T) {
	r := newRefusal(t)
	live, err := LiveRefusedAdvances(r.cfg)
	require.NoError(t, err)
	require.Len(t, live, 1, "the record starts live, or the drop below proves nothing")

	// Move the pin out from under the record, without running an upgrade.
	mgr := remote.NewLockfileManager(r.baseDir)
	lock, err := mgr.Load()
	require.NoError(t, err)
	entry, ok := lock.GetEntry(remote.ItemTypeBundle, r.ref)
	require.True(t, ok)
	require.Equal(t, r.kept, entry.SHA)
	entry.SHA = "0000000000000000000000000000000000000000"
	lock.AddEntry(remote.ItemTypeBundle, r.ref, entry)
	require.NoError(t, mgr.Save(lock))

	live, err = LiveRefusedAdvances(r.cfg)
	require.NoError(t, err)
	assert.Empty(t, live, "a record describing a pin the lockfile no longer holds is stale and must be dropped, not reported")
}

// The same drop for the coarser case: the dependency is gone from the lock
// entirely (dropped from the profile, or re-locked without it). A second entry
// is added first so the write is not an erasing one (ErrLockfileWouldErase) —
// the guard under test is the RECORD's, not the lockfile's.
func TestRefusals_ARecordForAnEntryNoLongerLockedIsNotReported(t *testing.T) {
	r := newRefusal(t)

	mgr := remote.NewLockfileManager(r.baseDir)
	lock, err := mgr.Load()
	require.NoError(t, err)
	lock.AddEntry(remote.ItemTypeBundle, "file:///elsewhere@bundles/other", remote.LockEntry{SHA: "abc123", URL: "file:///elsewhere"})
	lock.RemoveEntry(remote.ItemTypeBundle, r.ref)
	require.NoError(t, mgr.Save(lock))

	live, err := LiveRefusedAdvances(r.cfg)
	require.NoError(t, err)
	assert.Empty(t, live)
}

// An absent file is the ordinary "nothing has been refused" state. A file that
// EXISTS and cannot be read is not: folding it onto silence would print the
// same clean bill of health an empty store does, over a record nobody checked
// — the exact disappearance this store exists to prevent.
func TestRefusals_AnUnreadableRecordIsReportedNotReadAsSilence(t *testing.T) {
	tmp := t.TempDir()
	baseDir := filepath.Join(tmp, ".ctxloom")
	cfg := testConfigWithSCMPath(baseDir)

	live, err := LiveRefusedAdvances(cfg)
	require.NoError(t, err, "no file at all is not a fault")
	assert.Empty(t, live)

	path := paths.RefusedAdvancesPath(baseDir)
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte("version: 99\nrefusals: []\n"), 0o644))
	_, err = LiveRefusedAdvances(cfg)
	require.Error(t, err, "a version this build does not understand must be reported, never read as an empty store")
	assert.Contains(t, err.Error(), "version 99")

	require.NoError(t, os.WriteFile(path, []byte("\tnot: [yaml\n"), 0o644))
	_, err = LiveRefusedAdvances(cfg)
	require.Error(t, err, "an unparseable record must be reported")
}

// The configured() guard, in admission.Store's shape and for its reason:
// filepath.Join("", x) == x, so a project root that never resolved would read
// and write a record under the process working directory — one belonging to
// whatever directory the user happened to be standing in, then reported as
// this project's state.
func TestRefusals_AnUnresolvedProjectRootRefusesRatherThanUsingTheWorkingDirectory(t *testing.T) {
	cases := map[string]*config.Config{
		"no config":      nil,
		"no app paths":   config.NewFixture(config.Fixture{}),
		"empty app path": config.NewFixture(config.Fixture{AppPaths: []string{""}}),
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := LiveRefusedAdvances(cfg)
			assert.Error(t, err, "reading must refuse rather than resolve relative to the working directory")
			assert.Error(t, saveRefusedAdvances(cfg, []RefusedAdvance{{Identity: "x", KeptSHA: "a", ProposedSHA: "b"}}),
				"writing must refuse too")
		})
	}
}
