package cli

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/errs"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/remote"
)

// recordingPuller is a consumer-side fake for pullRunner. It records each
// call's ref and options and returns a per-ref scripted result or error.
type recordingPuller struct {
	errByRef map[string]error
	calls    []string
	opts     []remote.PullOptions
}

func (r *recordingPuller) Pull(_ context.Context, refStr string, opts remote.PullOptions) (*remote.PullResult, error) {
	r.calls = append(r.calls, refStr)
	r.opts = append(r.opts, opts)
	if err, ok := r.errByRef[refStr]; ok {
		return nil, err
	}
	return &remote.PullResult{SHA: "0000000updated"}, nil
}

func bundleUpd(ref string) updateInfo {
	return updateInfo{Type: remote.ItemTypeBundle, Ref: ref, CurrentSHA: "ccccccc", LatestSHA: "ddddddd"}
}

func TestApplyUpdates_AllSucceed(t *testing.T) {
	var out bytes.Buffer
	p := &recordingPuller{}
	updated, failed, removed := applyUpdates(context.Background(), &out, p,
		[]updateInfo{bundleUpd("bob/tools")})

	if updated != 1 || failed != 0 {
		t.Fatalf("updated=%d failed=%d, want 1/0", updated, failed)
	}
	if len(removed) != 0 {
		t.Fatalf("removed=%v, want none", removed)
	}
	if got := out.String(); !strings.Contains(got, "Updated to 0000000") {
		t.Fatalf("output missing success line:\n%s", got)
	}
}

func TestApplyUpdates_PullsAtPinnedSHA(t *testing.T) {
	var out bytes.Buffer
	p := &recordingPuller{}
	applyUpdates(context.Background(), &out, p, []updateInfo{bundleUpd("bob/tools")})

	// Each bundle is pulled at its exact constraint-bounded LatestSHA (a
	// "<ref>@<sha>" pin), not the bare ref.
	if want := []string{"bob/tools@ddddddd"}; !equalStrings(p.calls, want) {
		t.Fatalf("call order = %v, want %v", p.calls, want)
	}
}

// TestApplyUpdates_PinsSHAButPreservesConstraint verifies the version-model
// invariant: apply pulls the displayed constraint-bounded SHA for content, while
// the manifest constraint is carried through to the lock (so "^1.2" is not frozen
// into a SHA). detect already re-pins within the constraint; lock would re-derive
// it, but apply must not corrupt the lock in the interim.
func TestApplyUpdates_PinsSHAButPreservesConstraint(t *testing.T) {
	var out bytes.Buffer
	p := &recordingPuller{}
	u := updateInfo{Type: remote.ItemTypeBundle, Ref: "bob/tools", LatestSHA: "bbbbbbb", RequestedVersion: "^1.2"}
	applyUpdates(context.Background(), &out, p, []updateInfo{u})

	if len(p.calls) != 1 || p.calls[0] != "bob/tools@bbbbbbb" {
		t.Fatalf("expected a single pinned pull bob/tools@bbbbbbb, got %v", p.calls)
	}
	rv := p.opts[0].RequestedVersion
	if rv == nil || *rv != "^1.2" {
		t.Fatalf("apply must preserve the manifest constraint in the lock; got RequestedVersion=%v", rv)
	}
}

func TestApplyUpdates_ClassifiesErrors(t *testing.T) {
	var out bytes.Buffer
	// errByRef keys are the pinned "<ref>@<LatestSHA>" forms apply now pulls
	// (bundleUpd sets LatestSHA "ddddddd").
	p := &recordingPuller{errByRef: map[string]error{
		"x/skip@ddddddd":   fmt.Errorf("wrap: %w", errs.ErrCancelled),
		"x/gone@ddddddd":   fmt.Errorf("wrap: %w", errs.ErrRemoteContentNotFound),
		"x/broken@ddddddd": fmt.Errorf("boom"),
	}}
	updated, failed, removed := applyUpdates(context.Background(), &out, p,
		[]updateInfo{bundleUpd("x/skip"), bundleUpd("x/gone"), bundleUpd("x/broken"), bundleUpd("x/ok")})

	if updated != 1 {
		t.Errorf("updated=%d, want 1 (only x/ok)", updated)
	}
	if failed != 1 {
		t.Errorf("failed=%d, want 1 (only x/broken)", failed)
	}
	if len(removed) != 1 || removed[0].Ref != "x/gone" {
		t.Errorf("removed=%v, want [x/gone]", removed)
	}
}

func TestApplyUpdates_EmptyIsNoop(t *testing.T) {
	var out bytes.Buffer
	p := &recordingPuller{}
	updated, failed, removed := applyUpdates(context.Background(), &out, p, nil)
	if updated != 0 || failed != 0 || removed != nil {
		t.Fatalf("got %d/%d/%v, want 0/0/nil", updated, failed, removed)
	}
	if len(p.calls) != 0 {
		t.Fatalf("expected no Pull calls, got %v", p.calls)
	}
}

func TestUpdateInfo_SelectorLabel(t *testing.T) {
	cases := []struct {
		u    updateInfo
		want string
	}{
		{updateInfo{Kind: remote.SelectorBranch, RequestedVersion: "main"}, "tracking branch main"},
		{updateInfo{Kind: remote.SelectorBranch, RequestedVersion: ""}, "tracking branch default branch"},
		{updateInfo{Kind: remote.SelectorVersion, RequestedVersion: "^1.2", Version: "v1.3.0"}, "range ^1.2 → v1.3.0"},
		{updateInfo{Kind: remote.SelectorVersion, RequestedVersion: "^1.2"}, "range ^1.2"},
	}
	for _, c := range cases {
		if got := c.u.selectorLabel(); got != c.want {
			t.Errorf("selectorLabel(%+v) = %q, want %q", c.u, got, c.want)
		}
	}
}

func TestPrintAvailableUpdates(t *testing.T) {
	var out bytes.Buffer
	printAvailableUpdates(&out, []updateInfo{bundleUpd("bob/tools")})
	got := out.String()
	for _, want := range []string{"Bundles:", "bob/tools", "Current:", "Latest:"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
}

func TestPrintAvailableUpdates_OmitsEmptySection(t *testing.T) {
	var out bytes.Buffer
	printAvailableUpdates(&out, nil)
	if got := out.String(); strings.Contains(got, "Bundles:") {
		t.Errorf("should not print Bundles header when empty:\n%s", got)
	}
}

func TestReportBundleIssues(t *testing.T) {
	var out bytes.Buffer
	reportBundleIssues(&out, &operations.BundleAnalysis{
		Warnings: []string{"could not read x"},
		Invalid:  []string{"bad-ref"},
		Missing:  []string{"acme/missing"},
		Orphans:  []string{"acme/orphan"},
	})
	got := out.String()
	for _, want := range []string{"Warning: could not read x", "bad-ref", "acme/missing", "acme/orphan"} {
		if !strings.Contains(got, want) {
			t.Errorf("output missing %q:\n%s", want, got)
		}
	}
}

func TestReportBundleIssues_EmptyIsSilent(t *testing.T) {
	var out bytes.Buffer
	reportBundleIssues(&out, &operations.BundleAnalysis{})
	if out.Len() != 0 {
		t.Errorf("expected no output, got %q", out.String())
	}
}

func TestReportMissingDefaults(t *testing.T) {
	var out bytes.Buffer
	reportMissingDefaults(&out, []string{"ghost"})
	if got := out.String(); !strings.Contains(got, "ghost") || !strings.Contains(got, "Nonexistent default profiles") {
		t.Errorf("unexpected output:\n%s", got)
	}

	out.Reset()
	reportMissingDefaults(&out, nil)
	if out.Len() != 0 {
		t.Errorf("expected no output for empty input, got %q", out.String())
	}
}

func TestReportRemovedFromRemote_HintsWhenNoCleanup(t *testing.T) {
	// updateCleanup defaults to false, so this exercises the no-cleanup branch
	// (which touches no filesystem).
	var out bytes.Buffer
	reportRemovedFromRemote(&out, afero.NewMemMapFs(), ".ctxloom", []updateInfo{bundleUpd("acme/gone")}, nil, nil, true)
	got := out.String()
	if !strings.Contains(got, "acme/gone") || !strings.Contains(got, "--cleanup") {
		t.Errorf("expected removed listing + cleanup hint:\n%s", got)
	}
}

func TestReportRemovedFromRemote_SkipsCleanupWhenReloadFailed(t *testing.T) {
	// --cleanup is set, but the post-apply lockfile reload failed
	// (cleanupAllowed=false), so the destructive RemoveLocalItems/Save is
	// skipped: the seeded file survives and the lockfile entry is NOT pruned,
	// preventing a stale snapshot from reverting the just-applied SHAs.
	prev := updateCleanup
	updateCleanup = true
	t.Cleanup(func() { updateCleanup = prev })

	fs := afero.NewMemMapFs()
	appDir := ".ctxloom"
	const goneRef = "https://github.com/acme/repo@bundles/gone"
	goneParsed, perr := remote.ParseReference(goneRef)
	if perr != nil {
		t.Fatalf("parse ref: %v", perr)
	}
	bundlePath := goneParsed.LocalPath(appDir, remote.ItemTypeBundle)
	if err := afero.WriteFile(fs, bundlePath, []byte("name: gone\n"), 0o644); err != nil {
		t.Fatalf("seed bundle file: %v", err)
	}

	lockManager := remote.NewLockfileManager(appDir, remote.WithLockfileFS(fs))
	lockfile := &remote.Lockfile{
		Bundles: map[string]remote.LockEntry{},
	}
	lockfile.AddEntry(remote.ItemTypeBundle, goneRef, remote.LockEntry{SHA: "deadbee"})

	var out bytes.Buffer
	reportRemovedFromRemote(&out, fs, appDir, []updateInfo{bundleUpd(goneRef)}, lockfile, lockManager, false)

	if exists, _ := afero.Exists(fs, bundlePath); !exists {
		t.Errorf("expected %s to survive when cleanup is gated off", bundlePath)
	}
	if _, ok := lockfile.GetEntry(remote.ItemTypeBundle, goneRef); !ok {
		t.Error("expected lockfile entry to be retained when cleanup is gated off")
	}
	got := out.String()
	if !strings.Contains(got, "Skipping --cleanup") {
		t.Errorf("expected a skip notice:\n%s", got)
	}
}

func TestReportRemovedFromRemote_EmptyIsSilent(t *testing.T) {
	var out bytes.Buffer
	reportRemovedFromRemote(&out, afero.NewMemMapFs(), ".ctxloom", nil, nil, nil, true)
	if out.Len() != 0 {
		t.Errorf("expected no output, got %q", out.String())
	}
}

func TestReportRemovedFromRemote_CleanupDeletesFileAndPrunesLockfile(t *testing.T) {
	// --cleanup branch: deletes the local file off the seam'd fs and removes
	// the entry from the lockfile, persisting via the manager's own fs seam.
	prev := updateCleanup
	updateCleanup = true
	t.Cleanup(func() { updateCleanup = prev })

	fs := afero.NewMemMapFs()
	appDir := ".ctxloom"
	const goneRef = "https://github.com/acme/repo@bundles/gone"
	goneParsed, perr := remote.ParseReference(goneRef)
	if perr != nil {
		t.Fatalf("parse ref: %v", perr)
	}
	bundlePath := goneParsed.LocalPath(appDir, remote.ItemTypeBundle)
	if err := afero.WriteFile(fs, bundlePath, []byte("name: gone\n"), 0o644); err != nil {
		t.Fatalf("seed bundle file: %v", err)
	}

	lockManager := remote.NewLockfileManager(appDir, remote.WithLockfileFS(fs))
	lockfile := &remote.Lockfile{
		Bundles: map[string]remote.LockEntry{},
	}
	lockfile.AddEntry(remote.ItemTypeBundle, goneRef, remote.LockEntry{SHA: "deadbee"})

	var out bytes.Buffer
	reportRemovedFromRemote(&out, fs, appDir, []updateInfo{bundleUpd(goneRef)}, lockfile, lockManager, true)

	if exists, _ := afero.Exists(fs, bundlePath); exists {
		t.Errorf("expected %s to be deleted", bundlePath)
	}
	if _, ok := lockfile.GetEntry(remote.ItemTypeBundle, goneRef); ok {
		t.Error("expected lockfile entry to be pruned")
	}
	got := out.String()
	if !strings.Contains(got, "Removed: "+bundlePath) {
		t.Errorf("expected removal line for %s:\n%s", bundlePath, got)
	}
	if !strings.Contains(got, "Updated lockfile (removed 1 entries)") {
		t.Errorf("expected lockfile-update line:\n%s", got)
	}

	// The pruned lockfile was persisted through the manager's fs seam.
	reloaded, err := lockManager.Load()
	if err != nil {
		t.Fatalf("reload lockfile: %v", err)
	}
	if _, ok := reloaded.GetEntry(remote.ItemTypeBundle, "acme/gone"); ok {
		t.Error("persisted lockfile should not contain the pruned entry")
	}
}

func TestReportRemovedFromRemote_CleanupToleratesMissingFile(t *testing.T) {
	// A file already gone is the NORMAL case in the reference-only model
	// (nothing is materialized to disk): no warning, the lockfile entry is
	// pruned, and the pruned lockfile is persisted — the save is gated on
	// entries pruned, never on files deleted.
	prev := updateCleanup
	updateCleanup = true
	t.Cleanup(func() { updateCleanup = prev })

	fs := afero.NewMemMapFs() // empty: the target file does not exist
	const goneRef = "https://github.com/acme/repo@bundles/gone"
	lockManager := remote.NewLockfileManager(".ctxloom", remote.WithLockfileFS(fs))
	lockfile := &remote.Lockfile{
		Bundles: map[string]remote.LockEntry{},
	}
	lockfile.AddEntry(remote.ItemTypeBundle, goneRef, remote.LockEntry{SHA: "deadbee"})

	var out bytes.Buffer
	reportRemovedFromRemote(&out, fs, ".ctxloom", []updateInfo{bundleUpd(goneRef)}, lockfile, lockManager, true)

	got := out.String()
	if strings.Contains(got, "Warning: failed to remove") {
		t.Errorf("missing file should not warn:\n%s", got)
	}
	if _, ok := lockfile.GetEntry(remote.ItemTypeBundle, goneRef); ok {
		t.Error("expected lockfile entry to be pruned even when file was absent")
	}
	if !strings.Contains(got, "Updated lockfile (removed 1 entries)") {
		t.Errorf("expected the pruned lockfile to be persisted:\n%s", got)
	}
	reloaded, err := lockManager.Load()
	if err != nil {
		t.Fatalf("reload lockfile: %v", err)
	}
	if _, ok := reloaded.GetEntry(remote.ItemTypeBundle, goneRef); ok {
		t.Error("persisted lockfile should not contain the pruned entry")
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
