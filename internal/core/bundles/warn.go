package bundles

import (
	"fmt"
	"strings"
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
// StaleSignatureAdvice composes the sentence an author is told when their local
// tree's signature — the SHA256SUMS manifest and its .sigs/ entry — does not
// (or cannot be shown to) cover its current files: the decision table's
// `local | invalid | *` row, which ADMITS.
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
	return fmt.Sprintf("the signature of bundle %s no longer covers its files (%s) — the bundle is local, so its content is still delivered, "+
		"but it can no longer be published as signed content: re-sign with `ctxloom bundle sign %s`",
		read.Bundle.Name, read.signatureDetail, read.Bundle.Name)
}

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
