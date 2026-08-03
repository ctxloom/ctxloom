package bundles

import (
	"io"
	"path/filepath"
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
//
// DEDUP is process-scoped; the SINK is not. Each emission takes the writer of
// the loader that asked, because "where do my diagnostics go" is that caller's
// decision (WithWarnWriter) while "have I said this already" is the process's.
// Holding a writer in here instead made the second question answer the first,
// and a loader that redirected its diagnostics still lost these two to stderr.
type bundleWarner struct {
	mu   sync.Mutex
	seen map[warnKey]struct{}
}

// warnKey namespaces a warner's dedup set by warning KIND as a separate field,
// not as a prefix on the name. The two kinds key on different things — a bundle
// ref and a fragment name — and a shared string keyspace makes a ref that
// happens to spell another kind's key silence it.
type warnKey struct {
	kind string
	name string
}

func newBundleWarner() *bundleWarner {
	return &bundleWarner{seen: make(map[warnKey]struct{})}
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

// unresolved warns to out that ref did not resolve, once per ref for this
// warner's life.
func (b *bundleWarner) unresolved(out io.Writer, ref string, err error) {
	if !b.first("unresolved", ref) {
		return
	}
	clidiag.Fwarn(out, "ctxloom", "skipping unresolved bundle %q: %v", ref, err)
}

// ambiguous warns to out that a bare fragment ask matched several bundles, once
// per name for this warner's life.
func (b *bundleWarner) ambiguous(out io.Writer, name string, matches []string, chosen string) {
	if !b.first("ambiguous", name) {
		return
	}
	clidiag.Fwarn(out, "ctxloom", "fragment %q exists in multiple bundles (%s); using %s — qualify the ref to pick explicitly",
		name, strings.Join(matches, ", "), chosen)
}

// unresolvedBundleWarner is the process-wide dedup set. It holds no writer:
// every emission goes to the warn writer of the loader that asked.
var unresolvedBundleWarner = newBundleWarner()

// warnUnresolvedBundle and warnAmbiguousFragment are the loader's only route to
// the process-wide warner, so every one of its user-facing diagnostics honours
// WithWarnWriter (os.Stderr by default) exactly as fsStore.Save's does.
func (l *Loader) warnUnresolvedBundle(ref string, err error) {
	unresolvedBundleWarner.unresolved(l.warnOut, ref, err)
}

func (l *Loader) warnAmbiguousFragment(name string, matches []string, chosen string) {
	unresolvedBundleWarner.ambiguous(l.warnOut, name, matches, chosen)
}

func (l *Loader) warnStaleSignature(path, name, detail string) {
	unresolvedBundleWarner.staleSignature(l.warnOut, path, name, detail)
}

// staleSignature warns to out that path's sibling `.sig` does not (or cannot be
// shown to) cover path's current bytes, once per path for this warner's life.
//
// The wording states the outcome first — the content is DELIVERED — because the
// reader's first question on seeing the word "signature" in a warning is "did I
// just lose my context?", and for local content the answer is always no. What
// they have actually lost is the ability to publish it, which is what the
// remedy names.
// name is the bundle's resolution name (ExtractBundleName), never the file's
// basename: a directory-form bundle's file is always "bundle.yaml", and telling
// the user to run `ctxloom bundle sign bundle` would be a remedy that fails.
func (b *bundleWarner) staleSignature(out io.Writer, path, name, detail string) {
	if !b.first("stale-signature", path) {
		return
	}
	base := filepath.Base(path)
	clidiag.Fwarn(out, "ctxloom", "%s%s no longer covers %s (%s) — the bundle is local, so its content is still delivered, "+
		"but publishing it would be refused: re-sign with `ctxloom bundle sign %s`",
		base, SigSuffix, base, detail, name)
}
