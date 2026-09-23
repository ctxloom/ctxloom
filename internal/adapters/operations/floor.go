package operations

import (
	"fmt"

	"github.com/ctxloom/ctxloom/internal/adapters/remote"
)

// downgradeSet is the refs the operator named with --allow-downgrade, keyed the
// way the lockfile keys them. A downgrade is accepted ONLY for a named ref:
// there is deliberately no blanket form, because a flag that waived every
// floor at once would waive the one an attacker is moving along with the one
// the operator meant.
type downgradeSet map[string]struct{}

func newDowngradeSet(refs []string) (downgradeSet, error) {
	out := make(downgradeSet, len(refs))
	for _, r := range refs {
		ref, err := remote.ParseReference(r)
		if err != nil || !ref.IsCanonical() {
			return nil, fmt.Errorf("--allow-downgrade %q: not a canonical bundle reference (<repo-url>@bundles/<name>)", r)
		}
		out[ref.LockKey()] = struct{}{}
	}
	return out, nil
}

// allows reports whether identity — a lockfile key or any ref that parses to
// one — was named.
func (d downgradeSet) allows(identity string) bool {
	if len(d) == 0 {
		return false
	}
	if _, ok := d[identity]; ok {
		return true
	}
	ref, err := remote.ParseReference(identity)
	if err != nil {
		return false
	}
	_, ok := d[ref.LockKey()]
	return ok
}
