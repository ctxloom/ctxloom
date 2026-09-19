package bundles

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// bundleWarner emits the "unresolved bundle" warning at most once per distinct
// ref. Context is assembled more than once per process at startup — sync's
// context-file regeneration and the launch-time assembly — each over an
// independently resolved set, so dedup must outlive any single one of them.
// Hence a process-scoped warner rather than per-set state; the warning is pure
// diagnostics, so suppressing repeats after the first is safe.
//
// DEDUP is process-scoped; the SINK is not. Each emission takes the writer of
// the set that asked, because "where do my diagnostics go" is that caller's
// decision (WithWarnWriter) while "have I said this already" is the process's.
// Holding a writer in here instead makes the second question answer the first,
// and a caller that redirected its diagnostics loses these two to stderr.
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

// StaleSignatureAdvice composes the sentence an author is told when their local
// bundle's sibling `.sig` does not (or cannot be shown to) cover its current
// bytes — the decision table's `local | invalid | *` row, which ADMITS.
//
// It takes the READ rather than loose strings, so nothing can compose this
// sentence about content whose facts a reader did not establish.
//
// The wording states the OUTCOME first — the content is still delivered —
// because the reader's first question on seeing the word "signature" in a
// warning is "did I just lose my context?", and for local content the answer is
// always no. What they have actually lost is the ability to publish it as
// signed content, which is what the remedy names.
//
// The remedy names the bundle's RESOLUTION name, never the file's basename: a
// directory-form bundle's file is always "bundle.yaml", and telling the user to
// run `ctxloom bundle sign bundle` would be a remedy that fails.
//
// It is a STRING and not an emission on purpose. It rides Verdict.Detail, and
// the caller that received the verdict emits it — see Authorizer, "warnings ride the
// verdict".
func StaleSignatureAdvice(read BundleRead) string {
	if read.Bundle == nil {
		return ""
	}
	base := filepath.Base(read.Bundle.Path)
	return fmt.Sprintf("%s%s no longer covers %s (%s) — the bundle is local, so its content is still delivered, "+
		"but it can no longer be published as signed content: re-sign with `ctxloom bundle sign %s`",
		base, SigSuffix, base, read.signatureDetail, read.Bundle.Name)
}

// unresolvedBundleWarner is the process-wide dedup set. It holds no writer:
// every emission goes to the warn writer of the loader that asked.
var unresolvedBundleWarner = newBundleWarner()

// warnUnresolvedBundle and warnAmbiguousFragment are a resolved set's only
// route to the process-wide warner, so every one of its user-facing
// diagnostics honours WithWarnWriter (os.Stderr by default) exactly as
// fsStore.Save's does.
func (c Catalog) warnUnresolvedBundle(ref string, err error) {
	unresolvedBundleWarner.unresolved(c.warnWriter(), ref, err)
}

func (c Catalog) warnAmbiguousFragment(name string, matches []string, chosen string) {
	unresolvedBundleWarner.ambiguous(c.warnWriter(), name, matches, chosen)
}
