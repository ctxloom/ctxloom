package bundles

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/errs"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/shared/strictness"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// Catalog is the RESOLVED bundle set: everything a session can see, read once.
//
// It is a VALUE, and that is the whole point of it existing. A consumer holding
// a RESOLVER can re-read the world, and a consumer per call site means a re-read
// per call site. Holding a resolved set triggers no directory walk and no parse,
// so "who re-reads, and when" has exactly one answer.
//
// Pass this, not the resolver, wherever a caller only needs to ASK what is in
// the world rather than to rebuild it.
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

	// fs is the filesystem the local content in this set was read from, and
	// it is carried rather than re-derived because a skill's trust preimage
	// is computed from its on-disk tree: computing that preimage against a
	// DIFFERENT filesystem yields a different hash for the same skill and
	// withholds it in silence. The set therefore reads skill trees through
	// the same filesystem it resolved from, by construction.
	//
	// It is the first reader's filesystem in reader order (readersFS), which
	// makes reader ORDER decide it — the embedded builtin filesystem ahead of
	// the project's would redirect every project skill's preimage at a tree
	// that does not exist there.
	fs afero.Fs

	// failures is what the readers could say about a name they produced NO
	// read for: present but unparseable, keyed by the name asked. It is a
	// SNAPSHOT taken at resolve time, not a handle back to the readers, so
	// consulting it re-reads nothing.
	//
	// Without it "no such bundle" and "that bundle will not parse" collapse
	// into one verdict, and a corrupt file reads as a typo — pointing the
	// asker at their spelling instead of at the file they can fix.
	failures map[string]error

	// warnOut is where this set's user-facing diagnostics go; nil means
	// os.Stderr. WHERE they go is the caller's decision (WithWarnWriter) —
	// whether they have already been said is the process's (bundleWarner).
	warnOut io.Writer

	// candidates are the identities the readers established WITHOUT content.
	// They are a SECOND collection rather than nil-content entries in reads,
	// and that separation is the whole design: every Reads() consumer would
	// otherwise have to test BundleRead.Bundle for nil, and the one that
	// forgot would either panic or quietly treat an identity nobody read as a
	// bundle. Reads() holds only bundles that were actually obtained, and no
	// BundleRead in it has a nil Bundle.
	candidates []Candidate
}

// CandidateReason names why a candidate has an identity but no content.
//
// The three are different facts about the machine and imply different user
// actions — install it, allow it, fix it — so collapsing them would be the
// silent no-op this codebase's characteristic bug is made of.
type CandidateReason string

const (
	// CandidateUnconsented: the thing that would produce the content is
	// installed and this machine's human has not agreed ctxloom may run it.
	// Nothing was run, so nothing was read.
	CandidateUnconsented CandidateReason = "unconsented"
	// CandidateProbeFailed: obtaining the content was allowed and produced
	// nothing usable — the probe failed, timed out, or its output would not
	// parse.
	CandidateProbeFailed CandidateReason = "probe-failed"
	// CandidateAbsent: the identity is known but nothing on this machine
	// answers to it.
	CandidateAbsent CandidateReason = "absent"
)

// Candidate is a bundle identity that has NO content in this set.
//
// It exists because an identity can be established more cheaply than content.
// A companion's canonical ref is ctxloom+companion:<bin>, and <bin> comes from
// reading directory entries on $PATH — so a companion that is present but
// never consented to has a name, a location and a reason long before anything
// executes it. Reporting that state is what keeps "found but not run" from
// rendering as "not installed".
type Candidate struct {
	// Ref is the canonical identity the content WOULD be resolvable under,
	// were it ever obtained. It is the same key Catalog.LookupKey takes, so a
	// caller can ask whether a candidate later became a read.
	Ref trust.BundleKey
	// Path is where on this machine the content would come from, or "" when
	// nothing answers to the identity. It is what a remedy has to name — an
	// approval keys on the file, not on the name anything may claim.
	Path string
	// Reason is why there is no content.
	Reason CandidateReason
}

// CandidateReader is a Reader that also learns identities it obtained no
// content for.
//
// It is a SEPARATE interface, not a widened Reader, so a reader with nothing
// to say on the subject says nothing rather than returning an empty slice that
// a caller cannot tell from "this reader never looks". Candidates is valid
// only after Read has run: it reports what THAT pass established, and asking
// before it would answer about a pass that never happened.
type CandidateReader interface {
	Reader
	Candidates() []Candidate
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
	var candidates []Candidate

	for _, r := range readers {
		reads, err := r.Read(ctx)
		// Collected whatever Read answered, INCLUDING on error: a reader that
		// failed partway still knows which identities it found nothing for,
		// and dropping them would report a machine with no companions rather
		// than one whose companions could not be reached.
		if cr, ok := r.(CandidateReader); ok {
			candidates = append(candidates, cr.Candidates()...)
		}
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
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].Ref < candidates[j].Ref })
	return Catalog{
		reads:      reads,
		byKey:      byKey,
		candidates: candidates,
		fs:         readersFS(readers),
		failures:   readerFailures(readers),
	}
}

// ReadFailureReporter is a Reader that can say why a name it knows about
// produced no read.
//
// It is a SEPARATE interface, not a widened Reader, so a reader with nothing to
// say on the subject says nothing rather than returning an empty map a caller
// cannot tell from "this reader never records". The result is valid only after
// Read has run: it reports what THAT pass established.
type ReadFailureReporter interface {
	Reader
	ReadFailures() map[string]error
}

// readerFailures snapshots every read failure the readers recorded, keyed by
// the name asked.
//
// The FIRST reader to have recorded a name wins: readers are consulted in
// composition order, and an earlier one's account of a name is the one an ask
// for that name would reach.
func readerFailures(readers []Reader) map[string]error {
	var out map[string]error
	for _, r := range readers {
		rf, ok := r.(ReadFailureReporter)
		if !ok {
			continue
		}
		for name, err := range rf.ReadFailures() {
			if out == nil {
				out = make(map[string]error)
			}
			if _, seen := out[name]; !seen {
				out[name] = err
			}
		}
	}
	return out
}

// readersFS reports the filesystem a composed set of readers reads local
// content from: the first reader that has one, falling back to the OS
// filesystem when none does.
//
// ORDER decides it, and that is load-bearing rather than incidental. The
// builtin reader has a filesystem too — the EMBEDDED one — so composing it
// ahead of the project reader derives every project skill's trust preimage
// from a tree that does not exist there and withholds the skill in silence.
func readersFS(readers []Reader) afero.Fs {
	for _, r := range readers {
		if fsr, ok := r.(interface{ FS() afero.Fs }); ok {
			return fsr.FS()
		}
	}
	return afero.NewOsFs()
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

// Candidates returns every identity this set established WITHOUT content, in
// canonical-ref order.
//
// It is disjoint from Reads: a candidate is an identity nothing was read
// under, so nothing here appears there and no read here has to be checked for
// emptiness. A caller reporting on companions asks Reads for what is
// contributing and Candidates for what is not, and neither answer requires a
// second discovery pass over the machine.
//
// Building this list EXECUTES NOTHING. Every reason it carries was established
// by the same single pass that read the content — by looking at $PATH, at the
// consent record, and at what an already-permitted probe returned — so asking
// for it can never be the thing that runs a foreign binary.
//
// The copy is defensive, for the same reason Reads' is.
func (c Catalog) Candidates() []Candidate {
	return append([]Candidate(nil), c.candidates...)
}

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
// Every route here is EXACT. It mints the canonical identity first, which is
// the resolution the readers themselves stamp; only if that misses does it
// fall back to matching the spellings a listing can show — the ask as written,
// its canonical identity, and its fetch address — because such an identity
// addresses one bundle by construction and cannot be a name two bundles
// share.
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
	// The FETCH address too, because that is the spelling a listing shows for
	// a lockfile-seeded bundle: a lockfile keys on where content is fetched
	// from, and the reader stamps that key as the read's display name. Without
	// it a version-carrying ask would miss a seed that is present, and a
	// present bundle reported missing is content silently dropped.
	if parsed, err := remote.ParseReference(ask); err == nil {
		if key := parsed.LockKey(); key != ask {
			spellings = append(spellings, key)
		}
		parsed.ContentVersion = ""
		if key := parsed.LockKey(); key != ask {
			spellings = append(spellings, key)
		}
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
//
// It carries NO candidates. Provenance is a fact about a read, and a candidate
// has no read to carry one — so there is no honest answer to "which candidates
// are in this scope", and inventing one by passing them all through would let
// Scoped(ProvenanceProject).Candidates() report companions.
func (c Catalog) Scoped(classes ...ProvenanceClass) Catalog {
	keep := make(map[ProvenanceClass]bool, len(classes))
	for _, p := range classes {
		keep[p] = true
	}
	out := c
	out.reads, out.candidates = nil, nil
	out.byKey = make(map[trust.BundleKey]BundleRead)
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

// ListingNames returns the label each info is shown under, in the SAME order
// as infos so a renderer walks the two together by index.
//
// A row is shown under its Name. Two rows that would show the same Name are
// shown as "<name> (<uri>)" instead, because the bare name is then a handle
// that resolves to nothing: Catalog.Lookup refuses such an ask as ambiguous
// and names every candidate's canonical URI, so a listing that never prints
// those URIs leaves the reader holding a refusal whose remedy names a string
// the listing did not contain. The parenthetical is BundleInfo.Ref — byte-
// identical to what the refusal names — so it can be typed straight back.
//
// A single row keeps its bare name. Disambiguating a name nothing collides
// with would put a URI in front of every reader to fix an ambiguity that is
// not there, and the parenthetical would stop meaning "these two differ".
//
// A row with no canonical URI (an entry known only from the lockfile, whose
// content is gone upstream) has nothing to be told apart BY and keeps its bare
// name rather than rendering an empty parenthetical.
func ListingNames(infos []*BundleInfo) []string {
	shown := make(map[string]int, len(infos))
	for _, info := range infos {
		shown[info.Name]++
	}
	out := make([]string, 0, len(infos))
	for _, info := range infos {
		if shown[info.Name] > 1 && info.Ref != "" {
			out = append(out, fmt.Sprintf("%s (%s)", info.Name, info.Ref))
			continue
		}
		out = append(out, info.Name)
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

// FS returns the filesystem this set's local content was read from.
//
// A skill's trust preimage is derived from its on-disk tree
// (BundleSkill.ContentPayload), so a caller computing that preimage for an item
// this set resolved MUST use this same filesystem — computing it against a
// different one produces a different hash for the same skill and silently
// withholds it.
func (c Catalog) FS() afero.Fs {
	if c.fs == nil {
		return afero.NewOsFs()
	}
	return c.fs
}

// WithWarnWriter returns a copy of this set whose user-facing diagnostics go to
// w instead of os.Stderr, so a caller can read what the user would have been
// told. A warning nobody sees is the bug these diagnostics exist to prevent.
func (c Catalog) WithWarnWriter(w io.Writer) Catalog {
	c.warnOut = w
	return c
}

// warnWriter is the sink for this set's diagnostics.
func (c Catalog) warnWriter() io.Writer {
	if c.warnOut == nil {
		return os.Stderr
	}
	return c.warnOut
}

// Load reads a bundle by ask. See Lookup for what an ask may be and which asks
// are refused rather than resolved.
func (c Catalog) Load(ask string) (*Bundle, error) {
	read, err := c.read(ask)
	if err != nil {
		return nil, err
	}
	return read.Bundle, nil
}

// LoadKey reads a bundle by its EXACT resolution key. See LookupKey.
func (c Catalog) LoadKey(key trust.BundleKey) (*Bundle, error) {
	read, ok := c.LookupKey(key)
	if !ok {
		return nil, fmt.Errorf("%w: %s", errs.ErrBundleNotFound, key)
	}
	return read.Bundle, nil
}

// read resolves an ask to the read that answers for it, reporting an
// unresolved ask as what the readers know about it rather than as a bare
// absence.
func (c Catalog) read(ask string) (BundleRead, error) {
	read, err := c.Lookup(ask)
	if err != nil {
		return BundleRead{}, c.explain(ask, err)
	}
	return read, nil
}

// Read resolves an ask to the READ that answers for it — the content plus the
// trust facts its reader established.
//
// It exists because the decision function keys on those facts (Authorizer's
// Exposure carries a BundleRead), and the executable surfaces resolve a bundle
// by ref without ever going through a Pipeline. Load remains for callers that
// genuinely only want the bundle.
func (c Catalog) Read(ask string) (BundleRead, error) { return c.read(ask) }

// explain replaces the ONE resolution verdict a reader can say more about with
// what the reader knows: a bundle that is absent and a bundle whose file will
// not parse are different faults, and only the readers can tell them apart.
// Every other verdict — an ambiguous name, a retired spelling — is already the
// whole story and passes through untouched.
func (c Catalog) explain(ask string, err error) error {
	if errors.Is(err, errs.ErrBundleNotFound) {
		return c.missing(ask)
	}
	return err
}

// missing explains an ask that resolved to nothing.
//
// "Not found" and "found, but unreadable" are different facts and must not
// share an exit: a bundle whose file will not parse is a fault the asker can
// FIX, and reporting it as absent points them at their spelling instead of at
// the file.
func (c Catalog) missing(ask string) error {
	if err := c.failures[ask]; err != nil {
		return fmt.Errorf("bundle %s could not be read: %w", ask, err)
	}
	return fmt.Errorf("%w: %s", errs.ErrBundleNotFound, ask)
}

// Find locates the FILE backing a bundle. It exists for the two callers that
// need the path itself — deleting a bundle, and reporting whether a short name
// resolves — and refuses a bundle that has no file, because a synthetic path is
// not one.
func (c Catalog) Find(name string) (string, error) {
	if err := ValidateBundleName(name); err != nil {
		return "", err
	}
	read, err := c.read(name)
	if err != nil {
		return "", err
	}
	if read.Bundle.Path == "" || isSyntheticPath(read.Bundle.Path) {
		return "", fmt.Errorf("bundle %q has no file on this machine (it came from %s)", name, read.Provenance)
	}
	return read.Bundle.Path, nil
}
