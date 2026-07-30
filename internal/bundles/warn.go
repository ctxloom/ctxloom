package bundles

import (
	"io"
	"os"
	"strings"
	"sync"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// bundleWarner emits the "unresolved bundle" warning at most once per distinct
// ref. Context is assembled more than once per process at startup — sync's
// context-file regeneration and the launch-time assembly — each through an
// independent Loader, so dedup must outlive any single loader. Hence a
// process-scoped warner rather than per-Loader state; the warning is pure
// diagnostics, so suppressing repeats after the first is safe.
type bundleWarner struct {
	mu   sync.Mutex
	seen map[warnKey]struct{}
	out  io.Writer
}

// warnKey namespaces a warner's dedup set by warning KIND as a separate field,
// not as a prefix on the name. The two kinds key on different things — a bundle
// ref and a fragment name — and a shared string keyspace makes a ref that
// happens to spell another kind's key silence it.
type warnKey struct {
	kind string
	name string
}

func newBundleWarner(out io.Writer) *bundleWarner {
	return &bundleWarner{seen: make(map[warnKey]struct{}), out: out}
}

// first reports whether this (kind, name) has not been warned about yet, and
// records it. Callers hold no lock; this takes it.
func (b *bundleWarner) first(kind, name string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	key := warnKey{kind: kind, name: name}
	if _, ok := b.seen[key]; ok {
		return false
	}
	b.seen[key] = struct{}{}
	return true
}

// unresolved warns that ref did not resolve, once per ref for this warner's life.
func (b *bundleWarner) unresolved(ref string, err error) {
	if !b.first("unresolved", ref) {
		return
	}
	clidiag.Fwarn(b.out, "ctxloom", "skipping unresolved bundle %q: %v", ref, err)
}

// ambiguous warns that a bare fragment ask matched several bundles, once per
// name for this warner's life.
func (b *bundleWarner) ambiguous(name string, matches []string, chosen string) {
	if !b.first("ambiguous", name) {
		return
	}
	clidiag.Fwarn(b.out, "ctxloom", "fragment %q exists in multiple bundles (%s); using %s — qualify the ref to pick explicitly",
		name, strings.Join(matches, ", "), chosen)
}

// unresolvedBundleWarner is the process-wide default, writing to stderr.
var unresolvedBundleWarner = newBundleWarner(os.Stderr)
