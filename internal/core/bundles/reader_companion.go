package bundles

import (
	"context"
	"sort"
	"sync"

	"github.com/ctxloom/ctxloom/internal/shared/report"

	"github.com/ctxloom/ctxloom/internal/core/trust"
)

// CompanionLoadout is one companion application's advertised loadout, exactly
// as it came off that binary's stdout: the loadout document's bytes.
//
// It is BYTES, not a parsed loadout, on purpose: the reader parses and labels
// what it read in one place, by the thing that read the bytes.
type CompanionLoadout struct {
	// Bin is the companion's binary name, which is also its identity: the
	// loadout is seeded under the ctxloom:companion@<bin> ref.
	Bin string
	// Path is the resolved absolute path that was executed, for diagnostics.
	Path string
	// Document is the loadout document's raw bytes — ParseLoadout's input.
	Document []byte
	// Self marks the loadout ctxloom obtained from ITSELF: ctxloom is its
	// own companion, probed through the same exec as every other.
	Self bool
}

// CompanionProber obtains the loadouts of every companion this machine's human
// registered (ctxloom companion add).
//
// THE PROBER IS THE EXEC. Everything that decides whether a foreign binary
// runs at all — which names are registered, resolving each on PATH, the
// per-probe timeout — lives behind this one function, and the companion
// reader is the only thing in the read path that calls it. That is
// deliberate: the meaningful control point for companion content is EXEC,
// not content review (docs/trust-model.md, "Companion loadouts").
//
// It returns an error only for a fault that produced NO loadouts at all. An
// individual companion that is absent, wedged or does not implement
// the protocol never sinks the pass — never fatal, never a stalled startup —
// and is REPORTED as a CompanionCandidate instead: it was discovered, it has an
// identity, and saying nothing about it is what makes "found but never allowed
// to run" indistinguishable from "not installed".
type CompanionProber func(ctx context.Context) (CompanionProbe, error)

// CompanionCandidate is one registered companion the prober obtained NO
// loadout from, and why.
//
// It is the prober's other half, and it is a half only the prober can report:
// by the time the reader sees loadouts, a companion that is absent or wedged
// has left no trace at all. Bin is the identity (the reader mints
// ctxloom+companion:<bin> from it) and Path is the file a remedy has to name.
type CompanionCandidate struct {
	Bin    string
	Path   string
	Reason CandidateReason
}

// CompanionProbe is one companion-probing pass: the loadouts obtained, and
// the registered companions that yielded none.
//
// Both halves come from ONE pass over the registered names. That is
// the point of returning them together: a caller that wanted the second half
// separately would have to probe a second time, and two passes can disagree.
type CompanionProbe struct {
	Loadouts   []CompanionLoadout
	Candidates []CompanionCandidate
}

// companionReader reads the loadouts companion applications advertise about
// themselves: ProvenanceCompanion, TrustCtxLocal.
type companionReader struct {
	probe CompanionProber
	cfg   readerConfig

	// mu guards candidates, which Read replaces and Candidates reads. A
	// Loader memoizes its resolution but may be asked to re-resolve, and
	// nothing stops two goroutines holding the same Loader.
	mu         sync.Mutex
	candidates []Candidate
}

// NewCompanionReader reads every registered companion's loadout through probe.
//
// TrustCtxLocal, hard-coded and not a parameter: a loadout's bytes came
// straight off the stdout of a binary on the user's PATH, with no intermediary
// in between.
func NewCompanionReader(probe CompanionProber, opts ...ReaderOption) Reader {
	return &companionReader{probe: probe, cfg: newReaderConfig(opts)}
}

// companionRefPrefix is the source-ref scheme a companion loadout is seeded
// under. It matches remote.CompanionSource; it is spelled here rather than
// imported so this package's reader does not take a dependency on the remote
// ref parser to name its own content.
const companionRefPrefix = "ctxloom:companion@"

// Read reports every registered companion's loadout, in binary-name order so a
// session's contributed content is stable across runs.
//
// A loadout whose bytes will not PARSE produced no content at all: there is
// nothing to report, so it is warned about and skipped. That is not a policy
// drop — no bundle ever existed — and it is the "never crashes" half of the
// companion contract: an absent, timed-out or unparseable companion never sinks
// a session.
func (r *companionReader) Read(ctx context.Context) ([]BundleRead, error) {
	if r.probe == nil {
		return nil, nil
	}
	probe, err := r.probe(ctx)
	if err != nil {
		return nil, err
	}
	candidates := make([]Candidate, 0, len(probe.Candidates))
	for _, c := range probe.Candidates {
		if cand, ok := companionCandidate(r.cfg.rep, c.Bin, c.Path, c.Reason); ok {
			candidates = append(candidates, cand)
		}
	}
	out := make([]BundleRead, 0, len(probe.Loadouts))
	for _, lo := range probe.Loadouts {
		read, ok := r.read(lo)
		if !ok {
			// Bytes arrived and would not parse: the identity is real and the
			// binary ran, so this is a companion that produced nothing usable
			// rather than one that was never reached.
			if cand, ok := companionCandidate(r.cfg.rep, lo.Bin, lo.Path, CandidateProbeFailed); ok {
				candidates = append(candidates, cand)
			}
			continue
		}
		out = append(out, read)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ref < out[j].ref })
	r.mu.Lock()
	r.candidates = candidates
	r.mu.Unlock()
	return out, nil
}

// Candidates reports the companions this reader's last Read obtained no
// loadout from. It reads the probe's own account plus the loadouts that would
// not parse; it never probes, and there is nothing here it could execute.
func (r *companionReader) Candidates() []Candidate {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Candidate(nil), r.candidates...)
}

// companionCandidate mints one companion candidate's canonical identity from
// its binary NAME — the whole reason a candidate can exist at all:
// ctxloom+companion:<bin> needs the name a directory entry already gave, and
// no part of minting it runs the file.
//
// A name that will not mint has no identity to be reported under, so it is
// warned about and dropped rather than entered under an empty key where every
// unmintable name would stand in for every other.
func companionCandidate(rep report.Reporter, bin, path string, reason CandidateReason) (Candidate, bool) {
	typed, err := trust.CompanionRef(bin)
	if err != nil {
		warnUnmintableSource(rep, companionRefPrefix+bin, err)
		return Candidate{}, false
	}
	return Candidate{Ref: typed.BundleIdentity(), Path: path, Reason: reason}, true
}

// read turns one companion's loadout bytes into a read — the RUN bundle as
// the read's content, the typed INIT loadout beside it.
//
// Its diagnostics dedup, for the reason the sibling readers do: a process
// resolves the reader once per generation, and a later generation re-parses
// the SAME loadout bytes. Nothing it could say differs between generations.
func (r *companionReader) read(lo CompanionLoadout) (BundleRead, bool) {
	parsed, err := ParseLoadout(lo.Document)
	if err != nil {
		r.cfg.warnOnce("companion %q: unparseable loadout, withholding: %v", lo.Bin, err)
		return BundleRead{}, false
	}
	b := parsed.Run
	ref := companionRefPrefix + lo.Bin
	// The companion ref is the RESOLUTION identity and stays unconditional; it
	// is only the FALLBACK for Name, so a loadout that declared `name:` keeps
	// the name it declared.
	if b.Name == "" {
		b.Name = ref
	}
	typed, err := trust.CompanionRef(lo.Bin)
	if err != nil {
		warnUnmintableSource(r.cfg.rep, ref, err)
	}
	b.sourceRef = typed
	b.sourceRefSet = true

	b.self = lo.Self
	read := newRead(ref, b, ProvenanceCompanion, TrustCtxLocal)
	read.Init = parsed.Init
	return read, true
}
