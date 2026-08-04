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
// choke (bundles.fragmentContent/commandContent). It owns a review-records store
// built ONCE, so no item re-reads the countersignature stores, and resolves each
// item fail-closed: any parse/evaluation error withholds (returns false),
// matching the security mandate that "couldn't evaluate" must never mean
// "allow".
//
// That sharing covers APPROVALS ONLY. The retraction record is not shared and
// is re-read per item: retraction is left nil below (the production default),
// so EffectiveTrust builds it from the active lockfile on every call, opening
// and YAML-parsing lock.yaml once per gated item. Hoisting it would change WHEN
// retraction state is sampled, which is a trust decision rather than a caching
// one; measured and pinned in trust_perkitem_io_test.go.
//
// It receives the exact BYTES about to be exposed (pre-mustache) rather than a
// precomputed hash, so the decision can verify rather than merely compare —
// substitution only injects project-authored profile vars and cannot smuggle
// remote content past the gate.
type contentGate struct {
	cfg     *config.Config
	records ReviewRecords
	// retraction is a test-injection seam for the RETRACTION step (mirroring
	// records above): nil (the production default — buildContentGate never
	// sets it) lets
	// EffectiveTrust build its own default (the active lockfile, see
	// buildLockfileRetraction). Tests construct a *contentGate literal
	// directly (see gatedAcmeLoader, trust_exec_gate_test.go) and set this to
	// a fakeRetraction to exercise the retracted Source without touching a
	// real lockfile.
	retraction RetractionRecords
	fs         afero.Fs

	// withheld records every ref this gate denied, mapped to the FULL decided
	// result (Source + Detail) so the advisory can report WHY, not just THAT,
	// an item was withheld — a withhold must never be silent or reasonless
	// (docs/trust-model.md). The content loader surfaces its own withheld set
	// (loader.Withheld(), refs only), but the executable surfaces (MCP
	// servers, bundle hooks, prompt exports) call the gate directly with no
	// loader to tally them, so the gate keeps its own record and the caller
	// surfaces a content-free, reasoned advisory (ExecutableTrustGate.
	// WarnWithheld, warnWithheld).
	withheldMu sync.Mutex
	withheld   map[string]EffectiveTrustResult
}

// allow is the bundles.ContentGate the loader (and the executable resolvers)
// call per resolved item. It is fail-closed: any path that cannot positively
// justify exposure records the ref and withholds (returns false).
func (g *contentGate) allow(ref string, payload []byte, form, signer string) bool {
	tRef, _, _, err := parseTrustItemRef(ref)
	if err != nil {
		// A ref we cannot address cannot be trusted — withhold rather than expose
		// content the decision function never evaluated.
		clidiag.Warn("ctxloom", "trust gate: withholding %q (unparseable ref): %v", ref, err)
		g.record(ref, EffectiveTrustResult{Decision: trust.Deny, Source: trust.SourcePending})
		return false
	}
	res, err := EffectiveTrust(g.cfg, EffectiveTrustRequest{
		Ref:        tRef,
		Payload:    payload,
		Form:       form,
		Signer:     signer,
		Records:    g.records,
		Retraction: g.retraction,
		FS:         g.fs,
	})
	if err != nil || res == nil {
		clidiag.Warn("ctxloom", "trust gate: withholding %q (evaluation error): %v", ref, err)
		g.record(ref, EffectiveTrustResult{Decision: trust.Deny, Source: trust.SourcePending})
		return false
	}
	if res.Trusted() {
		return true
	}
	g.record(ref, *res)
	return false
}

// record marks ref as withheld with the FULL deciding result — Source plus any
// Detail (e.g. a retraction reason) — so a later advisory can name why, not
// just that, ref was withheld (deduplicated, lazily allocated).
func (g *contentGate) record(ref string, res EffectiveTrustResult) {
	g.withheldMu.Lock()
	if g.withheld == nil {
		g.withheld = make(map[string]EffectiveTrustResult)
	}
	g.withheld[ref] = res
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

// withheldItem pairs a withheld ref with the full trust decision that
// withheld it — enough for a caller to print a content-free line naming both
// the item and WHY (via Result.Reason()).
type withheldItem struct {
	Ref    string
	Result EffectiveTrustResult
}

// withheldItems returns every ref this gate withheld, paired with its
// deciding result, sorted by ref for stable output.
func (g *contentGate) withheldItems() []withheldItem {
	g.withheldMu.Lock()
	defer g.withheldMu.Unlock()
	if len(g.withheld) == 0 {
		return nil
	}
	out := make([]withheldItem, 0, len(g.withheld))
	for ref, res := range g.withheld {
		out = append(out, withheldItem{Ref: ref, Result: res})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Ref < out[j].Ref })
	return out
}

// buildContentGate constructs the *contentGate every real caller uses
// (exposureLoaderGated, NewExecutableTrustGate). It is the shared builder.
// records/fs are injection points for testing; production passes nil records
// and they are built from cfg (OS fs unless cfg carries an injected one).
// Returning the struct (not just g.allow) lets callers read the withheld
// tally afterward.
func buildContentGate(cfg *config.Config, records ReviewRecords, fs afero.Fs) *contentGate {
	g := &contentGate{cfg: cfg, fs: fs}
	if records == nil {
		records = newCountersignRecords(cfg, fs)
	}
	g.records = records
	return g
}

// ExecutableTrustGate gates the bundle EXECUTABLE surfaces — MCP servers and
// bundle hooks written to backend settings, plus prompt command-file exports —
// through the same per-item decision function as content. These surfaces bypass
// the content loader (they resolve via config.ResolveBundle* → WriteSettings,
// and backends.LoadCommandExports), so a rejection at the loader would be a
// no-op; each is gated at its OWN choke. The gate is injected into
// config.SetExecutableTrustGate (consulted by ResolveBundleMCPServers /
// ResolveBundleHooks / LoadCommandExports); a DENY omits the executable from
// settings (fail-closed). It tallies withheld refs so the caller can surface a
// content-free advisory.
//
// Construct ONCE per apply/run (it builds the review-records store up front).
// A nil *ExecutableTrustGate is a no-op (Gate() returns nil = no gating),
// matching the nil bundles.ContentGate convention.
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

// WarnWithheld surfaces one content-free advisory line PER bundle executable
// this gate withheld (MCP servers, hooks, prompt exports), naming the item and
// WHY — rejected, retracted by the publisher, or pending review — never a
// bare "withheld" (docs/trust-model.md: a withhold must never be silent or
// reasonless). Purely advisory (fault tolerance); a no-op when nothing was
// withheld.
func (e *ExecutableTrustGate) WarnWithheld() {
	if e == nil || e.gate == nil {
		return
	}
	warnWithheldItems(e.gate.withheldItems())
}

// exposureLoader returns the read-path bundle loader with the content trust
// gate attached. ONLY exposure surfaces use it — assembly, the ctxloom://
// fragment|prompt resources, fragment-reading hooks, and SessionStart regen — so
// management/listing paths keep using the gate-free bundleLoader and can still
// resolve pending content (to review, accept, or stamp it). cfg.FS() is
// threaded so the gate's review-records store reads the same filesystem as the
// rest of the operation (OS fs in production, a virtualized fs in tests).
func exposureLoader(cfg *config.Config, opts ...bundles.LoaderOption) *bundles.Loader {
	loader, _ := exposureLoaderGated(cfg, opts...)
	return loader
}

// exposureLoaderGated is exposureLoader's sibling: it builds the identical
// gated exposure loader but ALSO returns the underlying *contentGate, so a
// caller that reports why items were withheld (warnWithheld) can read each
// withheld ref's full decided result — Source and Detail — instead of just a
// bare ref list. Use this over exposureLoader whenever the caller goes on to
// call warnWithheld; callers that only load content (fragments.go, prompts.go
// — a single-item resource fetch that already returns a distinct withheld
// sentinel error, errs.ErrFragmentWithheld/ErrCommandWithheld) have no reasoned
// advisory to print and keep using the simpler exposureLoader.
func exposureLoaderGated(cfg *config.Config, opts ...bundles.LoaderOption) (*bundles.Loader, *contentGate) {
	gate := buildContentGate(cfg, nil, cfgFS(cfg))
	opts = append(opts, bundles.WithTrustGate(gate.allow))
	return cfg.SeededBundleLoader(opts...), gate
}

// cfgPreferDistilled returns the caller's raw-vs-distilled form choice, nil-safe.
//
// Form selection is a PROCESS-stage decision (docs/design/engine-delivery-seam.
// design.md, "ALL processing lives in the middle"): the read stage — the bundle
// loader — carries no preference at all, so every operation that actually reads
// item content names the form here, at the read. A nil cfg (an injected-loader
// test) gets the same default an unset setting does, which is distilled.
func cfgPreferDistilled(cfg *config.Config) bool {
	if cfg == nil {
		return true
	}
	return cfg.ShouldUseDistilled()
}

// cfgFS returns cfg's injected filesystem (nil for the OS default), nil-safe.
func cfgFS(cfg *config.Config) afero.Fs {
	if cfg == nil {
		return nil
	}
	return cfg.FS()
}

// warnWithheldItems emits one content-free advisory line PER withheld item —
// naming the item and WHY it was withheld (rejected, retracted by the
// publisher, or pending review), via EffectiveTrustResult.Reason() — so a
// withhold can never be silent or reasonless (docs/trust-model.md). Shared by
// the content-loader path (warnWithheld) and the executable path
// (ExecutableTrustGate.WarnWithheld) so both surfaces render identical
// wording for identical sources. A no-op for an empty set.
func warnWithheldItems(items []withheldItem) {
	for _, it := range items {
		clidiag.Warn("ctxloom", "withheld %s: %s", it.Ref, it.Result.Reason())
	}
}

// warnWithheld emits a content-free, reasoned advisory for the items the trust
// gate withheld during this assembly — one line per item, naming it and why
// (see warnWithheldItems) — pointing the user at `ctxloom review` where that is
// the action. It is purely advisory (fault tolerance: never an error) and a
// no-op when nothing was withheld.
//
// gate is the *contentGate the loader's trust gate was actually built from
// (see exposureLoaderGated) — it is a SUPERSET of loader.Withheld() (it also
// captures builtin-fragment gate calls, which bypass the loader's own content
// choke and so never reach loader.Withheld() at all — see
// config.ResolveBuiltinBundleFragments), so this also fixes a prior gap where
// a withheld builtin fragment surfaced no advisory whatsoever. Every real
// production call site (context.go, hooks.go, tooling.go) always builds
// through exposureLoaderGated, so gate is never nil in practice (the
// nil-gate/raw-loader degrade path was deleted along with warnPendingTally,
// its sole caller: that path only ever ran under a test-injected loader, and
// two of its three advisory branches — pending>0&&rejected>0, rejected-only —
// were unreachable even there, since the degrade always passed rejected=0).
func warnWithheld(gate *contentGate) {
	if gate == nil {
		return
	}
	warnWithheldItems(gate.withheldItems())
}
