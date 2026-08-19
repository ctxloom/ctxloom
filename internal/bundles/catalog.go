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
	reads []BundleRead // every read, in listing order

	// byKey is the ONE index, and its being one is the property that makes a
	// declared name unable to displace anything. The key is
	// BundleRead.Key() — the version-less, item-less canonical URI the
	// reader stamps from WHERE the bundle was read, never from what it
	// declares — so two bundles that share a declared name under different
	// URIs are two entries, both listed, both loadable, both independently
	// trustable. Resolution and trust land on one string by construction
	// rather than by agreement between two maps.
	//
	// A read whose SourceRef is the zero BundleRef (unaddressable — see
	// Ref.AsBundleRef for when a mint fails) is NOT entered: it has no
	// identity to be entered under, and letting every such read share the
	// empty key would make the last one in stand in for all the others. It
	// still appears in reads, so a listing shows it and the unmintable-source
	// report can name it; it is simply not resolvable, which is the same
	// fail-closed verdict its item refs already get.
	byKey map[trust.BundleKey]BundleRead
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
//
// Two reads that produce the SAME key — one reader emitting a bundle twice, or
// a pinned remote read superseding a stale extracted copy of the same URI — are
// resolved by plain last-wins, silently. That is an override between two
// spellings of ONE identity, which is precedence, not shadowing: there is no
// second bundle left unreachable to announce.
func Resolve(ctx context.Context, readers ...Reader) Catalog {
	byKey := make(map[trust.BundleKey]BundleRead)
	var order []trust.BundleKey
	var unaddressable []BundleRead

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
			if read.SourceRef().Class == "" {
				unaddressable = append(unaddressable, read)
				continue
			}
			key := read.Key()
			if _, seen := byKey[key]; !seen {
				order = append(order, key)
			}
			byKey[key] = read
		}
	}

	reads := make([]BundleRead, 0, len(order)+len(unaddressable))
	for _, key := range order {
		reads = append(reads, byKey[key])
	}
	reads = append(reads, unaddressable...)
	sortReads(reads)
	return Catalog{reads: reads, byKey: byKey}
}

// sortReads puts a resolved set in listing order: by the name a listing shows,
// then by canonical key so two bundles sharing a display name still order
// deterministically rather than by which reader happened to run first.
func sortReads(reads []BundleRead) {
	sort.SliceStable(reads, func(i, j int) bool {
		if reads[i].ref != reads[j].ref {
			return reads[i].ref < reads[j].ref
		}
		return reads[i].Key() < reads[j].Key()
	})
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

// Lookup resolves an ask to the read that answers for it.
//
// It returns an ERROR rather than a bool because `bool` cannot express
// AMBIGUOUS, and ambiguous is the one answer this function must be able to
// give. Two bundles may share a declared name under different canonical URIs;
// neither shadows the other, so an ask that names both resolves to nothing and
// says so, naming every candidate URI. A refusal cannot silently deliver the
// wrong bundle, which is what any winner-picking rule here could.
//
// The arms, in order:
//
//  1. ask parses as a canonical trust.BundleRef — resolved EXACTLY by
//     LookupKey. No search, no ambiguity possible. A canonical ref this
//     catalog does not hold is errs.ErrBundleNotFound.
//  2. ask is a self-contained identity in the pipeline's own spelling (a
//     lockfile ref, "ctxloom:local@bundles/<name>", "ctxloom:companion@<bin>")
//     — minted to its canonical key and resolved EXACTLY, again by LookupKey.
//     This arm is a bridge for the identities the assembly pipeline and the
//     lockfile still author; it never searches, so it cannot pick a winner.
//  3. ask carries the one scheme marker nothing still mints
//     (trust.IsRetiredBuiltinSpelling) — errs.ErrRetiredRefSpelling with the
//     migration hint. It is never downgraded to arm 4: "you typed a spelling
//     the grammar no longer accepts" and "no such bundle" are different faults
//     and deserve different messages. The WIDER ask-surface set
//     (trust.IsRetiredAskSpelling, used by ResolveAsk) must not be refused
//     here: arm 2's spellings are live identities on this path, and an
//     identity that resolves to nothing is a missing bundle, not a retired
//     spelling.
//  4. otherwise ask is a bare NAME. Every read is compared on the name a
//     listing shows (DisplayName), then on the bundle's DECLARED name.
//     Exactly one match resolves; zero is errs.ErrBundleNotFound; two or more
//     is errs.ErrBundleAmbiguous naming every candidate's canonical URI.
func (c Catalog) Lookup(ask string) (BundleRead, error) {
	if br, err := trust.ParseBundleRef(ask); err == nil {
		if read, ok := c.LookupKey(br.BundleIdentity()); ok {
			return read, nil
		}
		return BundleRead{}, fmt.Errorf("%w: %s", errs.ErrBundleNotFound, ask)
	}
	if remote.IsSelfContainedRef(ask) {
		if read, ok := c.selfContained(ask); ok {
			return read, nil
		}
	}
	if trust.IsRetiredBuiltinSpelling(ask) {
		return BundleRead{}, retiredSpelling(ask)
	}
	matches := c.matchingName(ask)
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return BundleRead{}, fmt.Errorf("%w: %s", errs.ErrBundleNotFound, ask)
	default:
		return BundleRead{}, ambiguousAsk(ask, matches)
	}
}

// selfContained resolves an ask written as a self-contained identity — a
// lockfile ref, "ctxloom:local@bundles/<name>", "ctxloom:companion@<bin>" —
// to the one read that identity names.
//
// These identities are AUTHORED, not legacy: a lockfile addresses a fetch and
// keys on "<url>@bundles/<path>", and profiles.ResolveProfile stamps
// remote.CanonicalBundleRef onto every resolved profile's SourceRef. Both
// reach Loader.Read, so this arm is what keeps a directory profile's hooks and
// MCP servers resolvable. It is not a migration bridge and does not expire
// with one.
//
// Every route here is EXACT. It mints the canonical key first, which is the
// resolution the readers themselves stamp; only if the identity was never
// mintable does it fall back to matching the spelling a listing shows, and
// then to the version-less form of it, because such an identity addresses one
// bundle by construction and cannot be a name two bundles share.
func (c Catalog) selfContained(ask string) (BundleRead, bool) {
	if br, err := canonicalBundleRefTyped(ask); err == nil {
		if read, ok := c.LookupKey(br.BundleIdentity()); ok {
			return read, true
		}
	}
	spellings := []string{ask}
	if key, ok := remote.CanonicalKey(ask); ok && key != ask {
		spellings = append(spellings, key)
	}
	for _, spelling := range spellings {
		for _, read := range c.reads {
			if read.DisplayName() == spelling {
				return read, true
			}
		}
	}
	return BundleRead{}, false
}

// matchingName reports every read a bare NAME could mean: the name a listing
// shows first, and only if nothing answers there the bundle's own declared
// name. The two are kept in that order rather than merged because a listing is
// a menu of handles, and a handle the listing showed must win over one it
// never did.
func (c Catalog) matchingName(ask string) []BundleRead {
	var byDisplayName, byDeclaredName []BundleRead
	for _, read := range c.reads {
		switch {
		case read.DisplayName() == ask:
			byDisplayName = append(byDisplayName, read)
		case read.Bundle != nil && read.Bundle.Name == ask:
			byDeclaredName = append(byDeclaredName, read)
		}
	}
	if len(byDisplayName) > 0 {
		return byDisplayName
	}
	return byDeclaredName
}

// ambiguousAsk refuses a name that means more than one bundle, naming every
// candidate by its canonical URI — the one spelling that tells them apart and
// the one the user can type back to say which they meant.
func ambiguousAsk(ask string, matches []BundleRead) error {
	var candidates strings.Builder
	for i, m := range matches {
		if i > 0 {
			candidates.WriteString(", ")
		}
		candidates.WriteString(string(m.Key()))
	}
	return fmt.Errorf("%w: %q names more than one bundle: %s",
		errs.ErrBundleAmbiguous, ask, candidates.String())
}

// retiredSpelling refuses an ask written in a reference grammar this version
// no longer accepts, and says where the current one is written down.
func retiredSpelling(ask string) error {
	return fmt.Errorf("%w: %q — see `ctxloom bundle trust --help`; re-run `ctxloom init` to migrate a project",
		errs.ErrRetiredRefSpelling, ask)
}

// LookupKey is the ONLY exact resolution: given the version-less canonical
// identity a read's SourceRef stamps, it either finds the one read that
// identity names or it does not. No search, no ambiguity.
func (c Catalog) LookupKey(key trust.BundleKey) (BundleRead, bool) {
	read, ok := c.byKey[key]
	return read, ok
}

// LookupRef is LookupKey for a caller holding a parsed reference rather than
// an already-extracted key. br's own item selector (Kind/Item), if any, is
// ignored: this resolves the BUNDLE the item lives in, matching
// BundleIdentity's own contract.
func (c Catalog) LookupRef(br trust.BundleRef) (BundleRead, bool) {
	return c.LookupKey(br.BundleIdentity())
}

// ResolveAsk resolves a user-typed bundle ASK to the canonical reference it
// names, without loading content. It is Lookup's identity-only half: a caller
// that needs the ref rather than the read (the trust mutations) uses this
// instead of discarding a BundleRead it never wanted.
//
// It is STRICTER than Lookup on one axis, deliberately. Lookup serves the load
// path, which still carries the pipeline's own self-contained identity
// spellings; ResolveAsk serves the surfaces where a human types a reference,
// and there a retired spelling is refused outright rather than bridged. The
// two agree on everything else, and share matchingName so an ambiguity is one
// rule rather than two.
//
// The arms:
//
//  1. ask parses as a canonical trust.BundleRef — resolved EXACTLY against
//     this catalog by LookupKey. A canonical ref that names no bundle HERE is
//     errs.ErrBundleNotFound: a bundle-level ask ("bundle show", "bundle
//     remove") is meaningless for a bundle the catalog cannot see.
//  2. ask carries a retired scheme marker — errs.ErrRetiredRefSpelling. Never
//     downgraded to a name search: a malformed or obsolete scheme-qualified
//     spelling must fail closed, not be silently re-read as a first-party
//     local bundle name.
//  3. otherwise ask is a bare NAME, resolved by matchingName. Exactly one
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
	if trust.IsRetiredAskSpelling(ask) {
		return trust.BundleRef{}, retiredSpelling(ask)
	}

	matches := c.matchingName(ask)
	switch len(matches) {
	case 1:
		return matches[0].SourceRef(), nil
	case 0:
		return trust.BundleRef{}, fmt.Errorf("%w: %s", errs.ErrBundleNotFound, ask)
	default:
		return trust.BundleRef{}, ambiguousAsk(ask, matches)
	}
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
	out := Catalog{byKey: make(map[trust.BundleKey]BundleRead)}
	for _, read := range c.reads {
		if !keep[read.Provenance] {
			continue
		}
		out.reads = append(out.reads, read)
		if read.SourceRef().Class != "" {
			out.byKey[read.Key()] = read
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
// Name is the read's DISPLAY NAME, not Bundle.Name, and the difference is
// load-bearing: a listing is a menu of handles the user types back at `bundle
// show`/`remove`, and the declared name need not be one. They diverge for any
// bundle outside the top level — lang/go.yaml is shown as "lang/go" while
// Bundle.Name is the leaf "go". Ref carries the canonical URI alongside it,
// which is what disambiguates two rows that share a Name.
func (c Catalog) Infos() []*BundleInfo {
	out := make([]*BundleInfo, 0, len(c.reads))
	for _, read := range c.reads {
		b := read.Bundle
		out = append(out, &BundleInfo{
			Name:          read.DisplayName(),
			Ref:           read.Key(),
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
