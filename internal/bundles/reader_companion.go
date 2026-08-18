package bundles

import (
	"context"
	"sort"
)

// CompanionLoadout is one companion application's advertised loadout, exactly
// as it came off that binary's stdout: the bundle document's bytes and the
// detached signature its release shipped alongside them.
//
// It is BYTES, not a parsed bundle, on purpose. Handing the reader a finished
// *Bundle with a signer already stamped would put the establishment of trust
// facts in the prober — i.e. in whichever caller wired it — and the whole point
// of a reader is that the facts are established in one place, by the thing that
// read the bytes.
type CompanionLoadout struct {
	// Bin is the companion's binary name, which is also its identity: the
	// loadout is seeded under the ctxloom:companion@<bin> ref.
	Bin string
	// Path is the resolved absolute path that was executed, for diagnostics.
	Path string
	// Bundle is the loadout document's raw bytes.
	Bundle []byte
	// Signature is the detached signature the envelope carried, or nil.
	Signature []byte
}

// CompanionProber obtains the loadouts of every companion this machine's human
// has agreed ctxloom may execute.
//
// THE PROBER IS THE EXEC. Everything about companion discovery that decides
// whether a foreign binary runs at all — the PATH scan, trust-on-first-use
// admission keyed on absolute path plus binary hash, the first-party exemption,
// the per-probe timeout — lives behind this one function, and the companion
// reader is the only thing in the read path that calls it. That is what makes
// the companion reader the single implementation that can prompt a human, and
// it is deliberate: the meaningful control point for companion content is
// EXEC, not content review (docs/trust-model.md, "Companion loadouts").
//
// It returns an error only for a fault that produced NO loadouts at all. An
// individual companion that is absent, wedged, unapproved or does not implement
// the protocol is skipped by the prober with a warning — never fatal, never a
// stalled startup.
type CompanionProber func(ctx context.Context) ([]CompanionLoadout, error)

// companionReader reads the loadouts companion applications advertise about
// themselves: ProvenanceCompanion, TrustCtxLocal.
type companionReader struct {
	probe CompanionProber
	cfg   readerConfig
}

// NewCompanionReader reads every admitted companion's loadout through probe.
//
// TrustCtxLocal, hard-coded and not a parameter: a loadout's bytes came
// straight off the stdout of a binary the user consented to execute, with no
// intermediary in between — and an intermediary is precisely the threat a
// publisher signature exists to catch. So the signature facts are still
// ESTABLISHED and REPORTED here (every reader tells the truth on all three
// axes) and they still do not gate: a companion loadout whose signature does
// not verify is delivered with a warning naming it, because dropping it would
// punish the user for the companion's build error. The control that catches a
// swapped binary is the hash-keyed exec consent inside the prober.
func NewCompanionReader(probe CompanionProber, opts ...ReaderOption) Reader {
	return &companionReader{probe: probe, cfg: newReaderConfig(opts)}
}

// companionRefPrefix is the source-ref scheme a companion loadout is seeded
// under. It matches remote.CompanionSource; it is spelled here rather than
// imported so this package's reader does not take a dependency on the remote
// ref parser to name its own content.
const companionRefPrefix = "ctxloom:companion@"

// Read reports every admitted companion's loadout, in binary-name order so a
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
	loadouts, err := r.probe(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]BundleRead, 0, len(loadouts))
	for _, lo := range loadouts {
		read, ok := r.read(lo)
		if !ok {
			continue
		}
		out = append(out, read)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ref < out[j].ref })
	return out, nil
}

// read turns one companion's loadout bytes into a read, establishing its
// signature facts and saying out loud what they were when they are not clean.
//
// All three diagnostics dedup, for the reason the sibling readers do: the probe
// behind them is memoized once per process, so every later loader build re-parses
// the SAME loadout bytes and re-checks the SAME signature. Nothing it could say
// differs between builds, and a process builds many loaders.
func (r *companionReader) read(lo CompanionLoadout) (BundleRead, bool) {
	b, err := ParseBundle(lo.Bundle)
	if err != nil {
		r.cfg.warnOnce("companion %q: unparseable loadout bundle, withholding: %v", lo.Bin, err)
		return BundleRead{}, false
	}
	ref := companionRefPrefix + lo.Bin
	// The companion ref is the RESOLUTION identity and stays unconditional; it
	// is only the FALLBACK for Name, so a loadout that declared `name:` keeps
	// the name it declared.
	if b.Name == "" {
		b.Name = ref
	}
	b.sourceRef = ref

	facts := readSignatureFacts(lo.Bundle, lo.Signature, r.cfg.root)
	switch {
	case facts.signature == SignatureInvalid:
		r.cfg.warnOnce("companion %q: its loadout signature does not verify over its own bytes — most likely a stale or "+
			"mismatched signature in the companion's release, so report it to the companion's authors; "+
			"delivering the content anyway, unattributed (%s)", lo.Bin, facts.detail)
	case facts.signer == SignerUntrusted:
		r.cfg.warnOnce("companion %q: its loadout is signed by a key this machine does not trust to publish; "+
			"delivering the content anyway (companion content is admitted at exec, not by signature), unattributed", lo.Bin)
	}
	facts.stamp(b)
	return newRead(ref, b, ProvenanceCompanion, TrustCtxLocal, facts), true
}
