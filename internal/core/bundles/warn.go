package bundles

import (
	"strings"
)

// warnUnresolvedBundle and warnAmbiguousFragment are Once findings: the same
// line about the same ref is noise the second time in one process, and
// whether it has already been said is the sink's business.
func (c Catalog) warnUnresolvedBundle(ref string, err error) {
	c.rep.WarnOncef("skipping unresolved bundle %q: %v", ref, err)
}

func (c Catalog) warnAmbiguousFragment(name string, matches []string, chosen string) {
	c.rep.WarnOncef("fragment %q exists in multiple bundles (%s); using %s — qualify the ref to pick explicitly",
		name, strings.Join(matches, ", "), chosen)
}
