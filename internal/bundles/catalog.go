package bundles

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/errs"
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

	// byTrustRef indexes the same reads by their TYPED source identity
	// (BundleRead.SourceRef().BundleIdentity()) rather than by resolution-ref
	// spelling. It is keyed alongside byRef, in the same loop, off the exact
	// same winning read after resolveCollision — so the two indexes can never
	// disagree about which read answers for a given resolution ref. A read
	// whose SourceRef is the zero BundleRef (unaddressable — see
	// Ref.AsBundleRef's doc for when a mint fails) is not entered here: an
	// empty BundleIdentity() would otherwise let every unaddressable read in
	// one resolve collide on ONE key, and the last one in would silently stand
	// in for all the others. See LookupBundleRef.
	byTrustRef map[string]BundleRead
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
	byTrustRef := make(map[string]BundleRead)
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
			winner := read
			prior, seen := byRef[read.ref]
			if !seen {
				order = append(order, read.ref)
				byRef[read.ref] = read
			} else {
				winner = resolveCollision(prior, read)
				byRef[read.ref] = winner
			}
			// Only a read with an ADDRESSABLE source ref is entered — see
			// byTrustRef's doc for why an unaddressable (zero) one is skipped
			// rather than colliding every such read onto one key.
			if src := winner.SourceRef(); src.Class != "" {
				byTrustRef[string(src.BundleIdentity())] = winner
			}
		}
	}

	sort.Strings(order)
	reads := make([]BundleRead, 0, len(order))
	for _, ref := range order {
		reads = append(reads, byRef[ref])
	}
	return Catalog{reads: reads, byRef: byRef, byTrustRef: byTrustRef}
}

// resolveCollision decides which of two reads sharing ONE resolution ref the
// catalog keeps, and reports a shadowing the user could not otherwise see.
//
// The default is unchanged and stays unchanged deliberately: a LATER reader
// wins, which is the precedence NewLoader documents and the reason pinned
// remote content shadows a stale extracted copy on disk. That is an intended
// override between two sources the user configured, and it stays silent.
//
// The ONE exception is a BUILTIN. Since a builtin's resolution ref stopped
// carrying its source class it can collide with a project bundle of the same
// name, and the builtin reader is composed AFTER the project reader (it must
// be — Loader.FS() returns the first reader that has a filesystem, and putting
// the embedded one first silently withholds every project skill). Plain
// last-wins would therefore let a bundle compiled into this binary displace a
// bundle the user wrote, which inverts every other precedence in the loader.
// So a builtin never displaces anything, and never loses silently:
//
//   - the PROJECT bundle wins and the builtin is shadowed
//   - the shadowing is ANNOUNCED, naming both TRUST refs — which is what still
//     distinguishes them once the resolution refs are one string — and telling
//     the user to rename one
//   - the session PROCEEDS
//
// Refusing was considered and rejected: a bundle published in ctxloom-default
// later adopting a name a project already uses would otherwise break that
// project on its next deps pull, over a clash the user did not create. Silent
// precedence was rejected as this project's characteristic silent-no-op shape
// (taskloom concerned-path, decided by the human 2026-08-17).
//
// FailOnce rather than Fail, matching Resolve's sibling report above and for
// the same measured reason: a process builds MANY catalogs (doctor went
// through 22), and a name collision is a property of the filesystem and the
// binary, so it cannot resolve itself between two builds in one process.
// Reporting per build buries the one line that names the bundle to rename.
// The FINDING still records per checkpoint window, so strict mode cannot be
// talked out of aborting by a repeat.
func resolveCollision(prior, incoming BundleRead) BundleRead {
	priorBuiltin := prior.Provenance == ProvenanceBuiltin
	incomingBuiltin := incoming.Provenance == ProvenanceBuiltin
	if priorBuiltin == incomingBuiltin {
		return incoming // ordinary last-wins precedence; not a shadowing
	}
	winner, shadowed := incoming, prior
	if incomingBuiltin {
		winner, shadowed = prior, incoming
	}
	strictness.FailOnce(strictness.ClassBundle,
		"rename one of the two bundles so each has its own name",
		"bundle %q resolves to two different bundles: %s (used) shadows %s (unreachable)",
		winner.ref, winner.SourceRef().String(), shadowed.SourceRef().String())
	return winner
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

// Lookup resolves an ask to a read, accepting the spellings of one identity
// that callers actually author: the bare name, the remote-qualified and
// canonical forms, and the local canonical form the assembly pipeline carries.
//
// There is no builtin arm, and its absence is the point. It existed to find a
// builtin whose resolution ref had been minted "builtin:<name>" — a ref that
// encoded WHERE the bundle sat, which is a trust question, not an addressing
// one. Nothing mints such a resolution ref any more, so the arm could only ever
// miss, and `builtin:isolation` is now correctly NOT a bundle handle: it is a
// TRUST ref (BundleRead.SourceRef), and the two are deliberately different
// strings. A bare `isolation` reaches the builtin, or the project's bundle of
// that name when one shadows it (resolveCollision).
//
// The three remaining arms are alias spellings of a ref the catalog already
// keys, not location encodings: remote-qualified "alice/go-tools" and the
// canonical/local-canonical forms the lockfile and the pipeline author.
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
	return BundleRead{}, false
}

// LookupBundleRef resolves a STRUCTURED bundle-level reference to the read
// that answers for it, keyed on the read's typed source identity
// (BundleRead.SourceRef().BundleIdentity()) rather than on any
// resolution-ref spelling.
//
// It is the typed counterpart to Lookup(name) for a caller that already
// holds a trust.BundleRef — the shape the canonical bundle-reference grammar
// parses a "ctxloom+<class>:…" string into — and it is PURELY ADDITIVE:
// Lookup(name) is unchanged, keeps resolving the bare-name/alias spellings
// it always has, and gains no new arm here. br's own item selector (Kind/
// Item), if any, is ignored — this resolves the BUNDLE the item lives in,
// matching BundleIdentity's own "identity of the bundle that contains the
// item" contract, not the item within it.
func (c Catalog) LookupBundleRef(br trust.BundleRef) (BundleRead, bool) {
	read, ok := c.byTrustRef[string(br.BundleIdentity())]
	return read, ok
}

// LookupKey is LookupBundleRef keyed directly on the resolution key rather
// than on a structured reference — the exact, total resolution: given the
// version-less canonical identity a read's SourceRef stamps, it either finds
// the one read that identity names or it does not. No search, no ambiguity.
//
// It is ADDITIVE (U3b-3 S4): the catalog still indexes byTrustRef the same
// way it always has, this is only a new way in to the same map.
func (c Catalog) LookupKey(key trust.BundleKey) (BundleRead, bool) {
	read, ok := c.byTrustRef[string(key)]
	return read, ok
}

// LookupRef is LookupKey for a caller holding a parsed trust.BundleRef rather
// than an already-extracted key — the same body as LookupBundleRef, under the
// name U3b-3's design settles on. Both names resolve identically for now;
// LookupBundleRef is retired in a later slice once every caller has moved.
func (c Catalog) LookupRef(br trust.BundleRef) (BundleRead, bool) {
	return c.LookupKey(br.BundleIdentity())
}

// ResolveAsk resolves a user-typed bundle ASK to the canonical reference it
// names, without loading content. It is Lookup's identity-only half: a caller
// that needs the ref rather than the read (the trust mutations) uses this
// instead of discarding a BundleRead it never wanted.
//
// The three arms:
//
//  1. ask parses as a canonical trust.BundleRef (trust.ParseBundleRef) —
//     resolved EXACTLY against this catalog by LookupKey(br.BundleIdentity()).
//     A canonical ref that names no bundle in THIS catalog is
//     errs.ErrBundleNotFound: unlike operations.ResolveItemAsk, this
//     resolver always consults the catalog, because a bundle-level ask
//     ("bundle show", "bundle remove") is meaningless for a bundle the
//     catalog cannot see.
//  2. ask carries a retired scheme marker ("builtin:", "ctxloom:local@",
//     "ctxloom:companion@", "://", "git@") — errs.ErrRetiredRefSpelling.
//     Never downgraded to a name search: a malformed or obsolete
//     scheme-qualified spelling must fail closed, not be silently re-read as
//     a first-party local bundle name.
//  3. otherwise: ask is compared against every read's DisplayName (Ref())
//     first; if none match, against the bundle's DECLARED Name. Exactly one
//     match resolves; zero is errs.ErrBundleNotFound; two or more is
//     errs.ErrBundleAmbiguous, naming every candidate.
//
// ResolveAsk does not itself understand an item selector ("#<kind>/<item>");
// it resolves the BUNDLE half of an ask. operations.ResolveItemAsk splits the
// selector off before calling here.
func (c Catalog) ResolveAsk(ask string) (trust.BundleRef, error) {
	if br, err := trust.ParseBundleRef(ask); err == nil {
		if _, ok := c.LookupKey(br.BundleIdentity()); !ok {
			return trust.BundleRef{}, fmt.Errorf("%w: %s", errs.ErrBundleNotFound, ask)
		}
		return br, nil
	}
	if isRetiredAskSpelling(ask) {
		return trust.BundleRef{}, fmt.Errorf("%w: %q — see `ctxloom bundle trust --help`; re-run `ctxloom init` to migrate a project",
			errs.ErrRetiredRefSpelling, ask)
	}

	var byDisplayName, byDeclaredName []BundleRead
	for _, read := range c.reads {
		if read.Ref() == ask {
			byDisplayName = append(byDisplayName, read)
		} else if read.Bundle != nil && read.Bundle.Name == ask {
			byDeclaredName = append(byDeclaredName, read)
		}
	}
	matches := byDisplayName
	if len(matches) == 0 {
		matches = byDeclaredName
	}

	switch len(matches) {
	case 0:
		return trust.BundleRef{}, fmt.Errorf("%w: %s", errs.ErrBundleNotFound, ask)
	case 1:
		return matches[0].SourceRef(), nil
	default:
		var candidates strings.Builder
		for i, m := range matches {
			if i > 0 {
				candidates.WriteString(", ")
			}
			candidates.WriteString(m.SourceRef().String())
		}
		return trust.BundleRef{}, fmt.Errorf("%w: %q names more than one bundle: %s",
			errs.ErrBundleAmbiguous, ask, candidates.String())
	}
}

// isRetiredAskSpelling reports whether ask carries a scheme marker belonging
// to a RETIRED (pre-U3b-3) or non-bundle reference spelling — a token that
// must fail closed rather than be downgraded to a bare-name search. It is
// remote.IsSelfContainedRef's list (ctxloom:local@, ctxloom:companion@,
// git@, any "://") plus "builtin:", the one retired spelling
// IsSelfContainedRef never had to know about because nothing outside the
// bundle-ask grammar ever minted it.
func isRetiredAskSpelling(ask string) bool {
	return strings.HasPrefix(ask, "builtin:") || remote.IsSelfContainedRef(ask)
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
	out := Catalog{byRef: make(map[string]BundleRead), byTrustRef: make(map[string]BundleRead)}
	for _, read := range c.reads {
		if !keep[read.Provenance] {
			continue
		}
		out.reads = append(out.reads, read)
		out.byRef[read.ref] = read
		if src := read.SourceRef(); src.Class != "" {
			out.byTrustRef[string(src.BundleIdentity())] = read
		}
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
