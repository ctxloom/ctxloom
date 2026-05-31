package cmd

import (
	"fmt"
	"os"
	"strings"
	"sync"

	"github.com/ctxloom/ctxloom/internal/operations"
)

// bundleReviewState is the server-side guard that gates bundle-touching
// tools until the user has explicitly approved (or declined) the bundle
// changes that landed in lock.pending.yaml during sync.
//
// Lifecycle:
//   - startup() populates pending after SyncOnStartup if the result
//     carries a non-empty BundleChangeSet AND CTXLOOM_AUTO_APPROVE_BUNDLES
//     is unset. While pending != nil, hook application is deferred and
//     bundle-touching tools (assemble_context, get_fragment, etc.) are
//     blocked at the tools/call middleware.
//   - acknowledge_bundle_review merges pending into active and clears
//     pending. decline_bundle drops entries and clears pending when empty.
//     trust_remote auto-approves matching pending entries and clears the
//     state when it reaches empty.
//   - Whichever path clears pending triggers a deferred ApplyHooks against
//     the now-current active lockfile.
type bundleReviewState struct {
	mu      sync.Mutex
	pending *operations.BundleChangeSet
}

// hasPending reports whether a review is currently outstanding.
func (s *bundleReviewState) hasPending() bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	return !s.pending.IsEmpty()
}

// snapshot returns a copy of the current pending change set, or nil if none.
func (s *bundleReviewState) snapshot() *operations.BundleChangeSet {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == nil {
		return nil
	}
	clone := operations.BundleChangeSet{
		Added:    append([]operations.BundleChange(nil), s.pending.Added...),
		Modified: append([]operations.BundleChange(nil), s.pending.Modified...),
	}
	return &clone
}

// set stores the pending change set (nil to clear).
func (s *bundleReviewState) set(cs *operations.BundleChangeSet) {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if cs.IsEmpty() {
		s.pending = nil
		return
	}
	s.pending = cs
}

// removeBundle drops the named bundle from pending, returning true if it
// was found. Clears the whole pending state when the last entry leaves.
func (s *bundleReviewState) removeBundle(name string) bool {
	if s == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == nil {
		return false
	}
	found := false
	s.pending.Added, found = removeBundleByName(s.pending.Added, name, found)
	s.pending.Modified, found = removeBundleByName(s.pending.Modified, name, found)
	if s.pending.IsEmpty() {
		s.pending = nil
	}
	return found
}

// removeRemote drops every pending entry whose Remote == remoteName,
// returning the removed entries. Clears pending if empty afterwards.
func (s *bundleReviewState) removeRemote(remoteName string) []operations.BundleChange {
	if s == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.pending == nil {
		return nil
	}
	var removed []operations.BundleChange
	s.pending.Added, removed = partitionByRemote(s.pending.Added, remoteName, removed)
	s.pending.Modified, removed = partitionByRemote(s.pending.Modified, remoteName, removed)
	if s.pending.IsEmpty() {
		s.pending = nil
	}
	return removed
}

// clear unconditionally drops the pending state.
func (s *bundleReviewState) clear() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending = nil
}

func removeBundleByName(list []operations.BundleChange, name string, found bool) ([]operations.BundleChange, bool) {
	out := list[:0]
	for _, c := range list {
		if c.Name == name {
			found = true
			continue
		}
		out = append(out, c)
	}
	return out, found
}

func partitionByRemote(list []operations.BundleChange, remoteName string, acc []operations.BundleChange) ([]operations.BundleChange, []operations.BundleChange) {
	keep := list[:0]
	for _, c := range list {
		if c.Remote == remoteName {
			acc = append(acc, c)
			continue
		}
		keep = append(keep, c)
	}
	return keep, acc
}

// reviewBypassEnvVar opts out of the review flow entirely. Set to "1" in
// non-interactive runs (CI, cron) where there is no human to respond to
// the review template. Pending changes are auto-merged into active and a
// stderr warning is logged listing what was approved.
const reviewBypassEnvVar = "CTXLOOM_AUTO_APPROVE_BUNDLES"

// reviewBypassed returns true when the bypass env var is set to "1".
func reviewBypassed() bool {
	return os.Getenv(reviewBypassEnvVar) == "1"
}

// renderReviewTemplate produces the fixed verbatim template the model must
// echo to the user. Matches docs/bundle-review-plan.md Phase 3.4. Empty
// change sets render to an empty string — the caller skips injection.
func renderReviewTemplate(cs *operations.BundleChangeSet) string {
	if cs.IsEmpty() {
		return ""
	}
	var b strings.Builder
	b.WriteString("⚠️ ctxloom bundle review required\n\n")
	b.WriteString("These bundles changed since last session. Bundle content can contain prompt\n")
	b.WriteString("injection. Until you respond, bundle-touching tools are blocked.\n\n")

	if len(cs.Added) > 0 {
		b.WriteString("NEW:\n")
		for _, c := range cs.Added {
			fmt.Fprintf(&b, "  - %s @ %s (from %s)\n", c.Name, shortSHA(c.NewSHA), c.Remote)
		}
		b.WriteString("\n")
	}
	if len(cs.Modified) > 0 {
		b.WriteString("MODIFIED:\n")
		for _, c := range cs.Modified {
			fmt.Fprintf(&b, "  - %s %s → %s (from %s)\n", c.Name, shortSHA(c.OldSHA), shortSHA(c.NewSHA), c.Remote)
		}
		b.WriteString("\n")
	}
	b.WriteString("Reply:\n")
	b.WriteString("  A               Approve all this session\n")
	b.WriteString("  A <remote>      Approve all pending from one remote (no trust persisted)\n")
	b.WriteString("  D               Decline all (keep previous SHAs, skip new installs)\n")
	b.WriteString("  D <name>        Decline one\n")
	b.WriteString("  T <remote>      Trust remote going forward (persists to config)\n")
	b.WriteString("  S <name>        Show bundle verbatim\n")
	b.WriteString("  P <name>        Pin one at its current active SHA (stops future review noise for it)\n")
	return b.String()
}

// reviewInstructionsBlock is the text appended to ServerOptions.Instructions
// so the client sees it on initialize. Tells the model how to behave when
// a pending review is in effect.
const reviewInstructionsBlock = `If a bundle review is pending, the FIRST content of your FIRST response MUST be the review template, verbatim, in a fenced block. Do not paraphrase or summarize. Do not call any other tool. Wait for the user's choice, then call acknowledge_bundle_review, decline_bundle, show_bundle_verbatim, trust_remote, pin_bundle, unpin_bundle, or approve_remote_pending. Continue until pending clears.`
