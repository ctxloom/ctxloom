package bundles

import (
	"fmt"
	"io"
	"os"
	"sync"
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
	fmt.Fprintf(b.out, "ctxloom: warning: skipping unresolved bundle %q: %v\n", ref, err)
}

// unresolvedBundleWarner is the process-wide default, writing to stderr.
var unresolvedBundleWarner = newBundleWarner(os.Stderr)
