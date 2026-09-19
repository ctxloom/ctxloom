package remote

import (
	"github.com/ctxloom/ctxloom/internal/core/trust"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
)

// LockfileRetraction implements composite.RetractionRecords over ONE read of
// the active lockfile: the LOCAL record of publisher retractions.
//
// Retraction status originates at the REMOTE manifest (CheckRetracted hits
// the fetcher) but the trust decision is an EXPOSURE-TIME, local-only
// decision — it must never make a network call of its own. So the network
// probe runs at pull time, its verdict is recorded in the lockfile, and this
// is what the gate reads back. It is built PER CONFIG GENERATION
// (config.Sources.TrustPorts), so a pull that rewrites the lockfile produces
// the next generation's records and never changes this one's.
type LockfileRetraction struct {
	lock       *Lockfile
	unreadable error
	path       string
}

// NewLockfileRetraction reads the lockfile lm manages, once. A lockfile that
// cannot be read is held as a FAULT rather than as an empty record: an
// unreadable file must withhold what travelled (Fault), never read as "nothing
// was retracted".
func NewLockfileRetraction(lm *LockfileManager) *LockfileRetraction {
	lockfile, err := lm.Load()
	if err != nil {
		return &LockfileRetraction{lock: &Lockfile{Bundles: map[string]LockEntry{}}, unreadable: err, path: lm.Path()}
	}
	return &LockfileRetraction{lock: lockfile}
}

// Retracted reports whether ref's bundle is recorded as retracted, and the
// publisher's stated reason (display-only, untrusted). A ref with no local
// record reports false: a missing retraction record is not itself a security
// gap, because a pull re-evaluates it for every installed ref.
func (l *LockfileRetraction) Retracted(ref trust.Ref) (bool, string) {
	if l == nil || l.lock == nil || ref.RepoURL == "" {
		return false, ""
	}
	entry, ok := l.lock.GetEntry(ItemTypeBundle, lockfileKeyForRef(ref))
	if !ok || !entry.Retracted {
		return false, ""
	}
	return true, entry.RetractedReason
}

// Fault implements composite.Faulted: why the lockfile could not be read, or
// nil. The gate asks only for content a retraction could cover, and this
// raises the trust finding once when it does — the file is left intact, so
// its holds and retractions can be read by hand first.
func (l *LockfileRetraction) Fault() error {
	if l == nil || l.unreadable == nil {
		return nil
	}
	strictness.FailOnce(strictness.ClassTrust,
		"delete "+l.path+" and rebuild it (ctxloom remote lock) — the file is left intact, so its holds and retractions can be read by hand first",
		"cannot establish retraction state: %s is unreadable (%v) — withholding remote content rather than treating a withdrawn bundle as trustworthy",
		l.path, l.unreadable)
	return l.unreadable
}

// lockfileKeyForRef renders the lockfile key a bundle item's ref resolves to:
// the same "<repo>@bundles/<name>" spelling the pull wrote the entry under.
func lockfileKeyForRef(ref trust.Ref) string {
	return ref.RepoURL + "@" + ItemTypeBundle.DirName() + "/" + ref.Bundle
}
