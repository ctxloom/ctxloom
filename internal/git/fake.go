package git

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// Fake is an in-memory Git double for unit-testing the highest-risk paths — the
// worktree lifecycle and the WIP-safe, nested-worktree-aware teardown — without a
// real repository. It records the sequence of mutating calls (Calls) so a test can
// assert ordering (inner-before-outer), and is driven by configurable state:
// which dirs are repos, what WorktreeList returns, and which dirs are dirty.
//
// It is exported (non-test file) so the isolation package's tests can inject it
// into the worktree policy. Concurrency-safe: the fan-out may share one.
type Fake struct {
	mu sync.Mutex

	// Repos are the directories IsRepo reports true for. When nil, IsRepo reports
	// true for every dir (the common single-repo test setup).
	Repos map[string]bool
	// Worktrees is what WorktreeList returns (repo-global).
	Worktrees []Worktree
	// Dirty maps a worktree path to whether IsDirty reports it as carrying WIP.
	Dirty map[string]bool
	// DirtyErr, when set, is returned by IsDirty instead of Dirty — the only
	// way a caller's fail-closed "unknown state" guard around IsDirty can be
	// unit-tested at all.
	DirtyErr error
	// IgnoredContent maps a worktree path to whether HasIgnoredContent
	// reports it as holding gitignored/excluded files.
	IgnoredContent map[string]bool
	// CommonDirValue is what CommonDir returns (empty → "<dir>/.git").
	CommonDirValue string
	// TrackedFiles is what ListTracked returns (the repo-tracked config files a
	// worktree carries); nil → none, so the skip-worktree pass is a no-op.
	TrackedFiles []string
	// LogEntries is what LogSince returns, keyed by the dir passed in ("" is
	// the fallback for any dir with no explicit entry) — mirrors Dirty's
	// per-path map shape.
	LogEntries map[string][]LogEntry
	// LogErr, when set, is returned by LogSince instead of LogEntries.
	LogErr error
	// Dirs is what RepoDirs returns; Changes is what WorkingChanges returns.
	Dirs    []string
	Changes []string
	// ChangesErr, when set, is returned by WorkingChanges instead of Changes —
	// the only way to unit-test a caller that renders a file list it could not
	// obtain, which is otherwise indistinguishable from "the tree is
	// dirty but nothing is listed".
	ChangesErr error

	// Error injectors: when set, the matching method fails (fault-tolerance tests).
	AddErr  error
	ListErr error

	// CurrentBranchValue is what CurrentBranch returns ("main" when empty);
	// CurrentBranchErr, when set, fails it instead.
	CurrentBranchValue string
	CurrentBranchErr   error

	// CommitAllSHA is the SHA CommitAll returns ("fake-commit-sha" when
	// empty); CommitAllChanged is the changed-file list it returns (the
	// commit's own self-reported verification — leave nil to simulate an
	// EMPTY commit landing, the case the dirty-tree commit handler must
	// refuse). CommitAllErr, when set, fails it instead. CommitMessages
	// records every message CommitAll was called with, in order, so a test
	// can assert on the exact generated commit message text.
	CommitAllSHA     string
	CommitAllChanged []string
	CommitAllErr     error
	CommitMessages   []string

	// DiffPatchValue is what DiffPatch returns.
	DiffPatchValue string

	// UntrackedList is what ListUntracked returns.
	UntrackedList []string

	// ApplyPatchErr, when set, fails ApplyPatch. AppliedPatches records every
	// patch ApplyPatch was called with, in order.
	ApplyPatchErr  error
	AppliedPatches []string

	// CloneErr/PushErr, when set, fail Clone/Push. The publish path drives
	// them to prove a failed clone or a failed push is never reported as a
	// successful publish.
	CloneErr error
	PushErr  error

	// HeadSHAValue is what HeadSHA returns ("fake-head-sha" when empty);
	// HeadSHAErr, when set, fails it instead.
	HeadSHAValue string
	HeadSHAErr   error

	// BlobSHAs maps "<ref>:<path>" to the blob SHA FileBlobSHA reports; a
	// missing key is the ABSENT answer ("" with no error), which is the
	// created-vs-updated discriminator. BlobSHAErr, when set, fails instead —
	// the only way to test a caller that must not read "could not ask" as
	// "not there".
	BlobSHAs   map[string]string
	BlobSHAErr error

	// RemoteRefSHAs maps "<remote>:<ref>" to what the REMOTE reports;
	// a missing key means the remote has no such ref. RemoteRefSHAErr, when
	// set, fails instead.
	RemoteRefSHAs   map[string]string
	RemoteRefSHAErr error

	// Calls records every mutating call in order, e.g. "add /tmp/wt",
	// "remove /tmp/wt/inner", "prune". Read for ordering assertions.
	Calls []string
	// Removed lists paths passed to WorktreeRemove (in order).
	Removed []string
}

var _ Git = (*Fake)(nil)

// record appends one call to the Calls log (caller holds mu).
func (f *Fake) record(s string) { f.Calls = append(f.Calls, s) }

// IsRepo reports true when Repos is nil (default) or explicitly marks dir.
func (f *Fake) IsRepo(dir string) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Repos == nil {
		return true
	}
	return f.Repos[dir]
}

// CommonDir returns CommonDirValue, or "<dir>/.git" when unset.
func (f *Fake) CommonDir(_ context.Context, dir string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.CommonDirValue != "" {
		return f.CommonDirValue, nil
	}
	return dir + "/.git", nil
}

// WorktreeAdd records the add and (unless AddErr is set) appends the new worktree
// to the list so a subsequent WorktreeList reflects it.
func (f *Fake) WorktreeAdd(_ context.Context, _, path, ref string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(fmt.Sprintf("add %s@%s", path, ref))
	if f.AddErr != nil {
		return f.AddErr
	}
	f.Worktrees = append(f.Worktrees, Worktree{Path: path, Detached: true})
	return nil
}

// WorktreeRemove records the removal, fails when the target is dirty
// (mirroring git's refusal — there is no force escape hatch, see
// Git.WorktreeRemove's doc), and otherwise drops the worktree from the list.
func (f *Fake) WorktreeRemove(_ context.Context, _, path string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(fmt.Sprintf("remove %s", path))
	if f.Dirty[path] {
		return fmt.Errorf("worktree %s contains modified or untracked files, use --force to delete it", path)
	}
	f.Removed = append(f.Removed, path)
	f.drop(path)
	return nil
}

// WorktreeList returns the configured list (a copy).
func (f *Fake) WorktreeList(_ context.Context, _ string) ([]Worktree, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ListErr != nil {
		return nil, f.ListErr
	}
	return append([]Worktree(nil), f.Worktrees...), nil
}

// WorktreePrune records the prune.
func (f *Fake) WorktreePrune(_ context.Context, _ string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record("prune")
	return nil
}

// UpdateIndexSkipWorktree records the skip-worktree toggle.
func (f *Fake) UpdateIndexSkipWorktree(_ context.Context, _, file string, skip bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(fmt.Sprintf("skip-worktree(%v) %s", skip, file))
	return nil
}

// ListTracked returns the configured tracked files (a copy). It does not record —
// a read, not a mutation — so call-ordering assertions stay focused on lifecycle
// writes.
func (f *Fake) ListTracked(_ context.Context, _ string, _ ...string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.TrackedFiles...), nil
}

// IsDirty reports the configured dirtiness for dir.
func (f *Fake) IsDirty(_ context.Context, dir string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.DirtyErr != nil {
		return false, f.DirtyErr
	}
	return f.Dirty[dir], nil
}

// LogSince returns the configured LogEntries for dir (falling back to the ""
// entry), bounded exactly as execGit bounds it (maxEntries<=0 means the
// package default, never "everything"). A read, like ListTracked and IsDirty —
// not recorded to Calls.
func (f *Fake) LogSince(_ context.Context, dir string, _ time.Time, maxEntries int) ([]LogEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.LogErr != nil {
		return nil, f.LogErr
	}
	entries := f.LogEntries[dir]
	if entries == nil {
		entries = f.LogEntries[""]
	}
	if maxEntries <= 0 {
		maxEntries = defaultLogSinceMax
	}
	if len(entries) > maxEntries {
		entries = entries[:maxEntries]
	}
	return append([]LogEntry(nil), entries...), nil
}

// RepoDirs returns the configured directory inventory (a copy), bounded
// exactly as execGit bounds it (maxDirs<=0 means the package default, never
// "everything"). A read — not recorded to Calls.
func (f *Fake) RepoDirs(_ context.Context, _ string, maxDirs int) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	dirs := f.Dirs
	if maxDirs <= 0 {
		maxDirs = defaultRepoDirsMax
	}
	if len(dirs) > maxDirs {
		dirs = dirs[:maxDirs]
	}
	return append([]string(nil), dirs...), nil
}

// WorkingChanges returns the configured porcelain changes (a copy), bounded
// exactly as execGit bounds it (maxEntries<=0 means the package default,
// never "everything"). A read — not recorded to Calls.
func (f *Fake) WorkingChanges(_ context.Context, _ string, maxEntries int) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ChangesErr != nil {
		return nil, f.ChangesErr
	}
	changes := f.Changes
	if maxEntries <= 0 {
		maxEntries = defaultWorkingChangesMax
	}
	if len(changes) > maxEntries {
		changes = changes[:maxEntries]
	}
	return append([]string(nil), changes...), nil
}

// CurrentBranch returns CurrentBranchValue, or "main" when unset.
func (f *Fake) CurrentBranch(_ context.Context, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.CurrentBranchErr != nil {
		return "", f.CurrentBranchErr
	}
	if f.CurrentBranchValue != "" {
		return f.CurrentBranchValue, nil
	}
	return "main", nil
}

// CommitAll records the message and (unless CommitAllErr is set) returns the
// configured CommitAllSHA/CommitAllChanged — a test drives the "empty
// commit" case by leaving CommitAllChanged nil.
func (f *Fake) CommitAll(_ context.Context, dir, message string) (string, []string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(fmt.Sprintf("commit-all %s", dir))
	f.CommitMessages = append(f.CommitMessages, message)
	if f.CommitAllErr != nil {
		return "", nil, f.CommitAllErr
	}
	sha := f.CommitAllSHA
	if sha == "" {
		sha = "fake-commit-sha"
	}
	return sha, append([]string(nil), f.CommitAllChanged...), nil
}

// DiffPatch returns the configured DiffPatchValue.
func (f *Fake) DiffPatch(_ context.Context, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.DiffPatchValue, nil
}

// ListUntracked returns the configured UntrackedList (a copy).
func (f *Fake) ListUntracked(_ context.Context, _ string) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.UntrackedList...), nil
}

// ApplyPatch records patch and returns ApplyPatchErr (nil = success). Unlike
// the real implementation it never actually applies anything — tests that
// need a REAL apply-and-verify round trip use initRepo + execGit directly.
func (f *Fake) ApplyPatch(_ context.Context, dir, patch string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(fmt.Sprintf("apply-patch %s", dir))
	if strings.TrimSpace(patch) == "" {
		return false, f.ApplyPatchErr
	}
	f.AppliedPatches = append(f.AppliedPatches, patch)
	if f.ApplyPatchErr != nil {
		return false, f.ApplyPatchErr
	}
	return true, nil
}

// Clone records the clone and returns CloneErr. It creates nothing on disk —
// tests that need a REAL clone/commit/push round trip drive execGit against a
// file:// bare repository, which is the only way to assert what actually
// landed on the far side.
func (f *Fake) Clone(_ context.Context, url, dir, branch string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(fmt.Sprintf("clone %s -> %s@%s", url, dir, branch))
	return f.CloneErr
}

// HeadSHA returns HeadSHAValue (or "fake-head-sha"), or HeadSHAErr.
func (f *Fake) HeadSHA(_ context.Context, _ string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.HeadSHAErr != nil {
		return "", f.HeadSHAErr
	}
	if f.HeadSHAValue != "" {
		return f.HeadSHAValue, nil
	}
	return "fake-head-sha", nil
}

// FileBlobSHA looks up "<ref>:<path>" in BlobSHAs; a miss is ABSENT.
func (f *Fake) FileBlobSHA(_ context.Context, _, ref, path string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.BlobSHAErr != nil {
		return "", f.BlobSHAErr
	}
	return f.BlobSHAs[ref+":"+path], nil
}

// Push records the push and returns PushErr.
func (f *Fake) Push(_ context.Context, dir, remote, refspec string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(fmt.Sprintf("push %s %s %s", dir, remote, refspec))
	return f.PushErr
}

// RemoteRefSHA looks up "<remote>:<ref>" in RemoteRefSHAs; a miss means the
// remote has no such ref.
func (f *Fake) RemoteRefSHA(_ context.Context, _, remote, ref string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.RemoteRefSHAErr != nil {
		return "", f.RemoteRefSHAErr
	}
	return f.RemoteRefSHAs[remote+":"+ref], nil
}

// HasIgnoredContent reports IgnoredContent[dir] (false when unconfigured).
func (f *Fake) HasIgnoredContent(_ context.Context, dir string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.IgnoredContent[dir], nil
}

// drop removes the worktree with the given path from the list (caller holds mu).
func (f *Fake) drop(path string) {
	out := f.Worktrees[:0]
	for _, w := range f.Worktrees {
		if w.Path != path {
			out = append(out, w)
		}
	}
	f.Worktrees = out
}
