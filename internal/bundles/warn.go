package bundles

import (
	"io"
	"os"
	"strings"
	"sync"

	"github.com/ctxloom/shared/clidiag"
)

// bundleWarner emits the "unresolved bundle" warning at most once per distinct
// ref. Context is assembled more than once per process at startup — sync's
// context-file regeneration and the launch-time assembly — each through an
// independent Loader, so dedup must outlive any single loader. Hence a
// process-scoped warner rather than per-Loader state; the warning is pure
// diagnostics, so suppressing repeats after the first is safe.
type bundleWarner struct {
	mu   sync.Mutex
	seen map[string]struct{}
	out  io.Writer
}

func newBundleWarner(out io.Writer) *bundleWarner {
	return &bundleWarner{seen: make(map[string]struct{}), out: out}
}

// unresolved warns that ref did not resolve, once per ref for this warner's life.
func (b *bundleWarner) unresolved(ref string, err error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.seen[ref]; ok {
		return
	}
	b.seen[ref] = struct{}{}
	clidiag.Fwarn(b.out, "ctxloom", "skipping unresolved bundle %q: %v", ref, err)
}

// ambiguous warns that a bare fragment ask matched several bundles, once per
// name for this warner's life.
func (b *bundleWarner) ambiguous(name string, matches []string, chosen string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	key := "ambiguous:" + name
	if _, ok := b.seen[key]; ok {
		return
	}
	b.seen[key] = struct{}{}
	clidiag.Fwarn(b.out, "ctxloom", "fragment %q exists in multiple bundles (%s); using %s — qualify the ref to pick explicitly",
		name, strings.Join(matches, ", "), chosen)
}

// unresolvedBundleWarner is the process-wide default, writing to stderr.
var unresolvedBundleWarner = newBundleWarner(os.Stderr)
