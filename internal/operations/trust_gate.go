package operations

import (
	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/remote"
	"github.com/ctxloom/ctxloom/internal/trust"
	"github.com/ctxloom/shared/clidiag"
)

// contentGate is the per-item trust gate (trust rework, TR5) bound into a
// bundle loader's content choke (bundles.fragmentContent/promptContent). It owns
// a trust store + remote registry built ONCE so the cascade resolves without
// per-item file I/O, and resolves each item fail-closed: any parse/evaluation
// error withholds (returns false), matching the security mandate that "couldn't
// evaluate" must never mean "allow".
//
// It deliberately does NOT recompute the content hash: the loader computes the
// effective-content hash of the exact bytes about to be exposed and passes it in,
// so the gate keys on what the agent would actually see (the pre-mustache
// EffectiveContent — substitution only injects project-authored profile vars and
// cannot smuggle remote content past the gate).
type contentGate struct {
	cfg      *config.Config
	store    *trust.Store
	registry *remote.Registry
	fs       afero.Fs

	// denyAll withholds everything. Set when the trust store cannot be opened —
	// it may hold a blacklist/denylist we must not silently skip (mirrors
	// EffectiveTrust's and TrustStamper's corrupt-store posture).
	denyAll bool
}

// allow is the bundles.ContentGate the loader calls per resolved item.
func (g *contentGate) allow(ref, contentHash, _ string) bool {
	if g.denyAll {
		return false
	}
	tRef, _, _, err := parseTrustItemRef(ref)
	if err != nil {
		// A ref we cannot address cannot be trusted — withhold rather than expose
		// content the cascade never evaluated.
		clidiag.Warn("ctxloom", "trust gate: withholding %q (unparseable ref): %v", ref, err)
		return false
	}
	res, err := EffectiveTrust(g.cfg, EffectiveTrustRequest{
		Ref:         tRef,
		ContentHash: contentHash,
		Store:       g.store,
		Registry:    g.registry,
		FS:          g.fs,
	})
	if err != nil || res == nil {
		clidiag.Warn("ctxloom", "trust gate: withholding %q (evaluation error): %v", ref, err)
		return false
	}
	return res.Trusted()
}

// newContentGate builds the TR5 fragment/prompt content gate for cfg. It runs the
// one-time migration baseline FIRST (idempotent, marker-guarded) so content
// present at rollout is trusted regardless of which entrypoint hits the gate
// first (follow-up #4: baseline-before-enforce on every entrypoint — MCP server,
// CLI run/oneshot, and the ctxloom:// resource paths). The store/registry/fs are
// injection points for testing; production passes nil and they are built from
// cfg (OS fs unless cfg carries an injected one).
func newContentGate(cfg *config.Config, store *trust.Store, registry *remote.Registry, fs afero.Fs) bundles.ContentGate {
	g := &contentGate{cfg: cfg, registry: registry, fs: fs}

	if store == nil {
		s, err := getTrustStore(cfg, nil, fs)
		if err != nil {
			// Fail closed: an unreadable store may hide a blacklist/denylist; deny
			// every gated item rather than degrade to allow-by-default.
			clidiag.Warn("ctxloom", "trust store unreadable; withholding all gated content: %v", err)
			g.denyAll = true
			return g.allow
		}
		store = s
	}
	g.store = store

	// Baseline-before-enforce: existing content must stay exposed at rollout.
	// Reuses the gate's own store so the minted grants are visible without a
	// second read. A baseline failure is warned, not fatal (fault tolerance: the
	// LLM still starts; un-baselined items simply gate — the safe direction).
	if _, err := BaselineTrust(cfg, BaselineTrustRequest{Store: store, FS: fs}); err != nil {
		clidiag.Warn("ctxloom", "trust baseline failed; some content may be withheld: %v", err)
	}

	if g.registry == nil {
		if reg, rerr := effectiveTrustRegistry(cfg, EffectiveTrustRequest{FS: fs}); rerr == nil {
			g.registry = reg
		}
		// else leave nil: EffectiveTrust rebuilds + fail-closes per call at the
		// remote tier without disturbing the earlier-tier precedence.
	}
	return g.allow
}

// exposureLoader returns the read-path bundle loader with the TR5 content trust
// gate attached. ONLY exposure surfaces use it — assembly, the ctxloom://
// fragment|prompt resources, fragment-reading hooks, and SessionStart regen — so
// management/listing paths keep using the gate-free bundleLoader and can still
// resolve untrusted content (to baseline it, grant it, or stamp it). cfg.FS()
// is threaded so the gate's trust store reads/writes the same filesystem as the
// rest of the operation (OS fs in production, a virtualized fs in tests).
func exposureLoader(cfg *config.Config, opts ...bundles.LoaderOption) *bundles.Loader {
	gate := newContentGate(cfg, nil, nil, cfgFS(cfg))
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

// warnWithheld emits a single content-free summary of the items the trust gate
// withheld during this assembly, pointing the user at the review/trust commands.
// It is purely advisory (fault tolerance: never an error) and a no-op for a
// gate-free loader or when nothing was withheld.
func warnWithheld(loader *bundles.Loader) {
	if loader == nil {
		return
	}
	if w := loader.Withheld(); len(w) > 0 {
		clidiag.Warn("ctxloom",
			"%d item(s) withheld by the trust gate — review with `ctxloom bundle show`, then trust with `ctxloom trust <ref>`",
			len(w))
	}
}
