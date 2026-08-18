package bundles

import (
	"context"
	"sort"

	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// Catalog is the RESOLVED bundle set: everything a session can see, read once.
//
// It is a VALUE, and that is the whole point of it existing. Every consumer used
// to hold a *Loader — a RESOLVER — which meant every consumer could re-read the
// world, and 22 of them did on a single `ctxloom doctor` run. A resolved set
// cannot: holding one triggers no directory walk and no parse, so the question
// "who re-reads, and when" has exactly one answer instead of one per call site.
//
// The reads were always here; what was missing was a NAME for the collection.
// Loader.Reads() returned a fresh []BundleRead copy on every call and nothing
// named it, so there was nothing to pass and everyone passed the resolver.
type Catalog struct {
	reads []BundleRead          // every read, in resolution order
	byRef map[string]BundleRead // resolution identity → read
}

// Resolve reads every reader ONCE and returns the set.
//
// This is the only function in the package that performs bundle I/O; everything
// on Catalog below is a query over its result.
//
// A reader that fails does not fail the resolve: the reads it produced before
// failing are kept, and the fault is reported once. A source that could not be
// read is NOT an empty source — reporting it is the difference between "you have
// no bundles" and "we could not find out what bundles you have".
func Resolve(ctx context.Context, readers ...Reader) Catalog {
	byRef := make(map[string]BundleRead)
	var order []string

	for _, r := range readers {
		reads, err := r.Read(ctx)
		if err != nil {
			// ONCE, because a process builds many resolved sets and a source
			// that fails deterministically — a malformed bundle file, which
			// cannot start parsing between two reads in one process — would
			// otherwise report once per build rather than once per fault. The
			// FINDING still records per checkpoint window, so strict mode cannot
			// be talked out of aborting by a repeat.
			strictness.FailOnce(strictness.ClassBundle, "check the source named in the error, then re-run",
				"a bundle source could not be read in full; some content may be missing: %v", err)
		}
		for _, read := range reads {
			if !admit(read) {
				continue
			}
			if _, seen := byRef[read.ref]; !seen {
				order = append(order, read.ref)
			}
			byRef[read.ref] = read
		}
	}

	sort.Strings(order)
	reads := make([]BundleRead, 0, len(order))
	for _, ref := range order {
		reads = append(reads, byRef[ref])
	}
	return Catalog{reads: reads, byRef: byRef}
}

// Reads returns every read in resolution order.
//
// The copy is defensive: a Catalog is handed out by value, but its slice header
// shares one backing array, so returning it directly would let any caller
// reorder or overwrite what everyone else sees.
func (c Catalog) Reads() []BundleRead {
	return append([]BundleRead(nil), c.reads...)
}

// Len reports how many reads the set holds, without copying it.
func (c Catalog) Len() int { return len(c.reads) }

// Lookup resolves an ask to a read, accepting every spelling of one identity:
// the bare name, the remote-qualified and canonical forms, the local canonical
// form the assembly pipeline carries, and — last — a builtin's qualified ref.
//
// The builtin fallback is LAST deliberately. When a project bundle and a builtin
// share a name both are addressable by their qualified refs, and the bare name
// keeps resolving to the project's: naming a bundle after a builtin is an
// override, not a collision.
func (c Catalog) Lookup(name string) (BundleRead, bool) {
	if read, ok := c.byRef[name]; ok {
		return read, true
	}
	if key, ok := remote.CanonicalKey(name); ok && key != name {
		if read, ok := c.byRef[key]; ok {
			return read, true
		}
	}
	if ref, err := remote.ParseReference(name); err == nil && ref.IsLocal && ref.ItemType == remote.ItemTypeBundle {
		if read, ok := c.byRef[ref.Path]; ok {
			return read, true
		}
	}
	if read, ok := c.byRef[trust.BuiltinSourcePrefix+name]; ok {
		return read, true
	}
	return BundleRead{}, false
}

// Scoped narrows to one or more provenance classes, returning a VIEW over the
// same reads rather than a second resolve.
//
// It replaces hand-rolled copies of the same filter that differed only in what
// they projected out of the read. Filtering a resolved set by provenance needs
// nothing but the reads themselves — no trust gate, no wire types — which is
// why it belongs here and the surface merging that CONSUMES it does not.
//
// The result shares this Catalog's reads; nothing here mutates them.
func (c Catalog) Scoped(classes ...ProvenanceClass) Catalog {
	keep := make(map[ProvenanceClass]bool, len(classes))
	for _, p := range classes {
		keep[p] = true
	}
	out := Catalog{byRef: make(map[string]BundleRead)}
	for _, read := range c.reads {
		if !keep[read.Provenance] {
			continue
		}
		out.reads = append(out.reads, read)
		out.byRef[read.ref] = read
	}
	return out
}

// Infos projects the set to listing metadata, in resolution order.
//
// It lives on Catalog rather than on Loader so a SCOPED listing is one call
// (Scoped(...).Infos()) instead of a hand-rolled loop per call site: BundleInfo
// deliberately carries no provenance, so narrowing after the projection is
// impossible and every narrowing caller would otherwise re-derive this loop.
//
// Name is the read's REF, not Bundle.Name, and the difference is load-bearing:
// a listing is a menu of handles the user types back at `bundle show`/`remove`,
// and only the ref resolves. They diverge for any bundle outside the top level —
// lang/go.yaml resolves as "lang/go" while Bundle.Name is the leaf "go".
func (c Catalog) Infos() []*BundleInfo {
	out := make([]*BundleInfo, 0, len(c.reads))
	for _, read := range c.reads {
		b := read.Bundle
		out = append(out, &BundleInfo{
			Name:          read.ref,
			Path:          b.Path,
			Version:       b.Version,
			Description:   b.Description,
			Tags:          b.Tags,
			FragmentCount: b.FragmentCount(),
			CommandCount:  b.CommandCount(),
			MCPCount:      b.MCPCount(),
			ProfileCount:  b.ProfileCount(),
			Signer:        b.Signer(),
		})
	}
	return out
}

// admit decides whether one read becomes addressable content, and is the ONLY
// place in the read path that can answer no.
//
// It holds exactly ONE rule, and that rule is not a policy about content: an
// UNCLAIMED read — one whose axes were never populated — is not content with
// unknown trust, it is a value nobody established anything about. Zero means
// unset and unset means withhold, or a struct literal would read as "local,
// unsigned, no signer".
//
// It stays HERE rather than moving to the Authorizer with the rest, and the
// asymmetry is deliberate. Every other rule decides about CONTENT and belongs to
// the process stage, where one verdict can serve the gate and the report alike.
// This one decides about a structurally invalid VALUE: there is no honest
// verdict to render for it, no user action it implies, and no reader that emits
// one. Keeping it at resolve time means such a value can never become
// addressable at ALL — a strictly stronger guarantee than withholding it at
// exposure, which would leave it resolvable by every management and listing path
// in between — and it costs nothing, because production can never reach it.
func admit(read BundleRead) bool {
	if !read.Claimed() {
		strictness.Fail(strictness.ClassTrust, "report this: a bundle reached the loader without established provenance",
			"withholding a bundle read that established no trust facts (provenance %s, context %s, signature %s, signer %s)",
			read.Provenance, read.trustCtx, read.signature, read.signer)
		return false
	}
	return true
}
