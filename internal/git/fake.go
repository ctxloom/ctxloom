package git

import (
	"context"
	"fmt"
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
	// CommonDirValue is what CommonDir returns (empty → "<dir>/.git").
	CommonDirValue string
	// ToplevelValue is what Toplevel returns (empty → the dir passed in).
	ToplevelValue string
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
	// RepoStateErr, when set, fails both RepoDirs and WorkingChanges.
	RepoStateErr error

	// Error injectors: when set, the matching method fails (fault-tolerance tests).
	AddErr    error
	ListErr   error
	RemoveErr error

	// Calls records every mutating call in order, e.g. "add /tmp/wt",
	// "remove(force=false) /tmp/wt/inner", "prune". Read for ordering assertions.
	Calls []string
	// Removed lists paths passed to WorktreeRemove (in order).
	Removed []string
}

var _ Git = (*Fake)(nil)

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

// Toplevel returns ToplevelValue, or dir when unset.
func (f *Fake) Toplevel(_ context.Context, dir string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.ToplevelValue != "" {
		return f.ToplevelValue, nil
	}
	return dir, nil
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

// WorktreeRemove records the removal (with its force flag), fails when the target
// is dirty and force is false (mirroring git's refusal), and otherwise drops the
// worktree from the list.
func (f *Fake) WorktreeRemove(_ context.Context, _, path string, force bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.record(fmt.Sprintf("remove(force=%v) %s", force, path))
	if f.RemoveErr != nil {
		return f.RemoveErr
	}
	if !force && f.Dirty[path] {
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
	return f.Dirty[dir], nil
}

// LogSince returns the configured LogEntries for dir (falling back to the ""
// entry), truncated to maxEntries when positive. A read, like ListTracked and
// IsDirty — not recorded to Calls.
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
	if maxEntries > 0 && len(entries) > maxEntries {
		entries = entries[:maxEntries]
	}
	return append([]LogEntry(nil), entries...), nil
}

// RepoDirs returns the configured directory inventory (a copy), truncated to
// maxDirs when positive. A read — not recorded to Calls.
func (f *Fake) RepoDirs(_ context.Context, _ string, maxDirs int) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.RepoStateErr != nil {
		return nil, f.RepoStateErr
	}
	dirs := f.Dirs
	if maxDirs > 0 && len(dirs) > maxDirs {
		dirs = dirs[:maxDirs]
	}
	return append([]string(nil), dirs...), nil
}

// WorkingChanges returns the configured porcelain changes (a copy), truncated
// to maxEntries when positive. A read — not recorded to Calls.
func (f *Fake) WorkingChanges(_ context.Context, _ string, maxEntries int) ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.RepoStateErr != nil {
		return nil, f.RepoStateErr
	}
	changes := f.Changes
	if maxEntries > 0 && len(changes) > maxEntries {
		changes = changes[:maxEntries]
	}
	return append([]string(nil), changes...), nil
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
