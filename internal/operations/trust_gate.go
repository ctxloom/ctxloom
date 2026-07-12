package operations

import (
	"sort"
	"sync"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// contentGate is the per-item trust gate bound into a bundle loader's content
// choke (bundles.fragmentContent/skillContent). It owns a trust store + remote
// registry built ONCE so the decision function resolves without per-item file
// I/O, and resolves each item fail-closed: any parse/evaluation error withholds
// (returns false), matching the security mandate that "couldn't evaluate" must
// never mean "allow".
//
// It receives the exact BYTES about to be exposed (pre-mustache) rather than a
// precomputed hash, so the decision can verify rather than merely compare —
// substitution only injects project-authored profile vars and cannot smuggle
// remote content past the gate.
type contentGate struct {
	cfg     *config.Config
	store   *trust.Store
	records ReviewRecords
	fs      afero.Fs

	// denyAll withholds everything. Set when the trust store cannot be opened —
	// it may hold rejections we must not silently skip (mirrors
	// EffectiveTrust's and TrustStamper's corrupt-store posture).
	denyAll bool

	// withheld records every ref this gate denied, mapped to the deciding
	// source (pending vs rejected) so the advisory can tally them apart. The
	// content loader surfaces its own withheld set (loader.Withheld()), but the
	// executable surfaces (MCP servers, bundle hooks, prompt exports) call the
	// gate directly with no loader to tally them, so the gate keeps its own
	// record and the caller surfaces a content-free advisory
	// (ExecutableTrustGate.WarnWithheld).
	withheldMu sync.Mutex
	withheld   map[string]trust.Source
}

// allow is the bundles.ContentGate the loader (and the executable resolvers)
// call per resolved item. It is fail-closed: any path that cannot positively
// justify exposure records the ref and withholds (returns false).
func (g *contentGate) allow(ref string, payload []byte, form, signer string) bool {
	if g.denyAll {
		g.record(ref, trust.SourcePending)
		return false
	}
	tRef, _, _, err := parseTrustItemRef(ref)
	if err != nil {
		// A ref we cannot address cannot be trusted — withhold rather than expose
		// content the decision function never evaluated.
		clidiag.Warn("ctxloom", "trust gate: withholding %q (unparseable ref): %v", ref, err)
		g.record(ref, trust.SourcePending)
		return false
	}
	res, err := EffectiveTrust(g.cfg, EffectiveTrustRequest{
		Ref:     tRef,
		Payload: payload,
		Form:    form,
		Signer:  signer,
		Store:   g.store,
		Records: g.records,
		FS:      g.fs,
	})
	if err != nil || res == nil {
		clidiag.Warn("ctxloom", "trust gate: withholding %q (evaluation error): %v", ref, err)
		g.record(ref, trust.SourcePending)
		return false
	}
	if res.Trusted() {
		return true
	}
	g.record(ref, res.Source)
	return false
}

// record marks ref as withheld with its deciding source (deduplicated, lazily
// allocated).
func (g *contentGate) record(ref string, source trust.Source) {
	g.withheldMu.Lock()
	if g.withheld == nil {
		g.withheld = make(map[string]trust.Source)
	}
	g.withheld[ref] = source
	g.withheldMu.Unlock()
}

// withheldRefs returns the refs this gate withheld, deduplicated and sorted.
func (g *contentGate) withheldRefs() []string {
	g.withheldMu.Lock()
	defer g.withheldMu.Unlock()
	if len(g.withheld) == 0 {
		return nil
	}
	out := make([]string, 0, len(g.withheld))
	for ref := range g.withheld {
		out = append(out, ref)
	}
	sort.Strings(out)
	return out
}

// withheldTally counts the withheld refs by disposition: pending (awaiting
// review) vs rejected (a human already declined them).
func (g *contentGate) withheldTally() (pending, rejected int) {
	g.withheldMu.Lock()
	defer g.withheldMu.Unlock()
	for _, src := range g.withheld {
		if src == trust.SourceRejected {
			rejected++
		} else {
			pending++
		}
	}
	return pending, rejected
}

// newContentGate builds the fragment/prompt content gate for cfg. The store/fs
// are injection points for testing; production passes nil and they are built
// from cfg (OS fs unless cfg carries an injected one).
func newContentGate(cfg *config.Config, store *trust.Store, fs afero.Fs) bundles.ContentGate {
	return buildContentGate(cfg, store, fs).allow
}

// buildContentGate constructs the *contentGate behind newContentGate (and the
// executable gate). It is the shared builder: it opens the trust store once,
// failing closed to denyAll when unreadable. Returning the struct (not just
// g.allow) lets the executable gate read the withheld tally afterward.
func buildContentGate(cfg *config.Config, store *trust.Store, fs afero.Fs) *contentGate {
	g := &contentGate{cfg: cfg, fs: fs}

	if store == nil {
		s, err := getTrustStore(cfg, nil, fs)
		if err != nil {
			// Fail closed: an unreadable store may hide rejections; deny every
			// gated item rather than degrade to allow-by-default.
			clidiag.Warn("ctxloom", "trust store unreadable; withholding all gated content: %v", err)
			g.denyAll = true
			return g
		}
		store = s
	}
	g.store = store
	return g
}

// ExecutableTrustGate gates the bundle EXECUTABLE surfaces — MCP servers and
// bundle hooks written to backend settings, plus prompt command-file exports —
// through the same per-item decision function as content. These surfaces bypass
// the content loader (they resolve via config.ResolveBundle* → WriteSettings,
// and backends.LoadSkillExports), so a rejection at the loader would be a
// no-op; each is gated at its OWN choke. The gate is injected into
// config.SetExecutableTrustGate (consulted by ResolveBundleMCPServers /
// ResolveBundleHooks / LoadSkillExports); a DENY omits the executable from
// settings (fail-closed). It tallies withheld refs so the caller can surface a
// content-free advisory.
//
// Construct ONCE per apply/run (it builds the trust store + registry up
// front). A nil *ExecutableTrustGate is a no-op (Gate() returns nil = no
// gating), matching the nil bundles.ContentGate convention.
type ExecutableTrustGate struct {
	gate *contentGate
}

// NewExecutableTrustGate builds the executable gate for cfg (production passes
// the OS fs via cfg).
func NewExecutableTrustGate(cfg *config.Config) *ExecutableTrustGate {
	return &ExecutableTrustGate{gate: buildContentGate(cfg, nil, cfgFS(cfg))}
}

// Gate returns the bundles.ContentGate the resolvers/loaders consult, or nil
// (no gating) for a nil receiver/gate.
func (e *ExecutableTrustGate) Gate() bundles.ContentGate {
	if e == nil || e.gate == nil {
		return nil
	}
	return e.gate.allow
}

// WarnWithheld surfaces one content-free advisory naming how many bundle
// executables this gate withheld (MCP servers, hooks, prompt exports), split
// into pending (awaiting review) and rejected. Purely advisory (fault
// tolerance); a no-op when nothing was withheld.
func (e *ExecutableTrustGate) WarnWithheld() {
	if e == nil || e.gate == nil {
		return
	}
	pending, rejected := e.gate.withheldTally()
	warnPendingTally(pending, rejected, "bundle executable(s)")
}

// exposureLoader returns the read-path bundle loader with the content trust
// gate attached. ONLY exposure surfaces use it — assembly, the ctxloom://
// fragment|prompt resources, fragment-reading hooks, and SessionStart regen — so
// management/listing paths keep using the gate-free bundleLoader and can still
// resolve pending content (to review, accept, or stamp it). cfg.FS() is
// threaded so the gate's trust store reads/writes the same filesystem as the
// rest of the operation (OS fs in production, a virtualized fs in tests).
func exposureLoader(cfg *config.Config, opts ...bundles.LoaderOption) *bundles.Loader {
	gate := newContentGate(cfg, nil, cfgFS(cfg))
	opts = append(opts, bundles.WithTrustGate(gate))
	return cfg.SeededBundleLoader(cfg.ShouldUseDistilled(), opts...)
}

// cfgFS returns cfg's injected filesystem (nil for the OS default), nil-safe.
func cfgFS(cfg *config.Config) afero.Fs {
	if cfg == nil {
		return nil
	}
	return cfg.FS()
}

// warnPendingTally emits the single content-free withheld advisory: N items
// awaiting review (plus a rejected count when a human already declined some),
// pointing at the one review destination, `ctxloom review`. A no-op when
// nothing was withheld.
func warnPendingTally(pending, rejected int, what string) {
	switch {
	case pending > 0 && rejected > 0:
		clidiag.Warn("ctxloom",
			"%d %s awaiting review (+%d rejected) — run 'ctxloom review'",
			pending, what, rejected)
	case pending > 0:
		clidiag.Warn("ctxloom", "%d %s awaiting review — run 'ctxloom review'", pending, what)
	case rejected > 0:
		clidiag.Warn("ctxloom", "%d rejected %s withheld", rejected, what)
	}
}

// warnWithheld emits a single content-free summary of the items the trust gate
// withheld during this assembly, pointing the user at the review/accept
// commands. It is purely advisory (fault tolerance: never an error) and a no-op
// for a gate-free loader or when nothing was withheld. The loader's withheld
// set carries no per-ref disposition, so the tally counts everything as
// awaiting review (rejected items are the rare subset; the executable gate
// splits them where it is cheap).
func warnWithheld(loader *bundles.Loader) {
	if loader == nil {
		return
	}
	if w := loader.Withheld(); len(w) > 0 {
		warnPendingTally(len(w), 0, "item(s)")
	}
}
