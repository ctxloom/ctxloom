package operations

import (
	"fmt"
	"github.com/ctxloom/ctxloom/internal/core/trust"

	"github.com/ctxloom/ctxloom/internal/adapters/remote"
)

// downgradeSet is the refs the operator named with --allow-downgrade, keyed the
// way the lockfile keys them. A downgrade is accepted ONLY for a named ref:
// there is deliberately no blanket form, because a flag that waived every
// floor at once would waive the one an attacker is moving along with the one
// the operator meant.
type downgradeSet map[trust.BundleKey]struct{}

func newDowngradeSet(refs []string) (downgradeSet, error) {
	out := make(downgradeSet, len(refs))
	for _, r := range refs {
		ref, err := remote.ParseReference(r)
		if err != nil || !ref.IsCanonical() {
			return nil, fmt.Errorf("--allow-downgrade %q: not a canonical bundle reference (<repo-url>@bundles/<name>)", r)
		}
		key, err := ref.LockKey()
		if err != nil {
			return nil, fmt.Errorf("--allow-downgrade %q: %w", r, err)
		}
		out[key] = struct{}{}
	}
	return out, nil
}

// allows reports whether the bundle with lock key key was named.
func (d downgradeSet) allows(key trust.BundleKey) bool {
	_, ok := d[key]
	return ok
}

// allowsRef is allows for a reference in any spelling: it is keyed through
// the reference's own LockKey, never by casting the string. A reference that
// names no bundle was not named.
func (d downgradeSet) allowsRef(ref string) bool {
	if len(d) == 0 {
		return false
	}
	parsed, err := remote.ParseReference(ref)
	if err != nil {
		return false
	}
	key, err := parsed.LockKey()
	if err != nil {
		return false
	}
	return d.allows(key)
}
