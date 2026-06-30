package operations

import (
	"time"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
	"github.com/ctxloom/ctxloom/internal/trust"
)

// BaselineTrustRequest drives the one-time TR6 migration snapshot. Store/Loader/
// FS are optional injection points for testing; production builds them from cfg.
type BaselineTrustRequest struct {
	Store  *trust.Store    `json:"-"`
	Loader *bundles.Loader `json:"-"`
	FS     afero.Fs        `json:"-"`
}

// BaselineTrustResult reports the snapshot outcome.
type BaselineTrustResult struct {
	// Status is "baselined" (the snapshot just ran) or "already" (a prior run
	// recorded it; this call was a no-op).
	Status string `json:"status"`
	// Granted is the number of trusted grants minted (currently-present remote
	// bundle items). Local content/executables are intentionally not granted.
	Granted int `json:"granted"`
	// Local is the number of enumerated items skipped because they are local
	// (project-authored): local fragments/prompts are auto-allowed by the
	// cascade and need no grant; local executables are never auto-trusted.
	Local int `json:"local"`
	// Failed is the number of items skipped because their content could not be
	// resolved/hashed. They are simply absent from the baseline and will gate
	// once enforcement lands — the safe (fail-closed) direction.
	Failed int `json:"failed"`
	// Total is every bundle item enumerated (Granted + Local + Failed).
	Total int `json:"total"`
}

// hashBaselineItem resolves an item's effective-content hash for the snapshot.
// Indirected through a package var (mirroring config.lookPath / sync*Step) so a
// test can simulate a per-item resolve/hash failure: the baseline must skip the
// failing item and continue, never abort.
var hashBaselineItem = computeItemHash

// BaselineTrust records the one-time per-item trust baseline (trust rework, TR6):
// it enumerates every currently-resolvable item across the active config and
// mints a trusted grant — keyed {canonical repo_url, ref, effective-content
// hash} — for each present REMOTE (bundle) item. This is the snapshot that lets
// enforcement (TR5) ship on-by-default without withholding content that already
// exists at rollout: only the snapshot members auto-grant; anything pulled later
// (or a post-baseline content swap of an existing ref → new hash) is absent and
// gates normally.
//
// It is behavior-preserving (TR6 adds no enforcement) and runs exactly once: a
// marker in trust.yaml guards re-runs, so a second invocation is a no-op (it
// does NOT re-grant post-baseline content). It is fault-tolerant: a per-item
// resolve/hash failure warns and skips that one item (it gates later, the safe
// direction); only an unreadable/unwritable store returns an error, which the
// startup caller turns into a warn-and-continue.
//
// Local content (fragments/prompts) is auto-allowed by the cascade and local
// executables (mcp / hooks) are never auto-trusted, so neither is recorded here
// — only remote bundle content needs a grant to survive the gate.
func BaselineTrust(cfg *config.Config, req BaselineTrustRequest) (*BaselineTrustResult, error) {
	store, err := getTrustStore(cfg, req.Store, req.FS)
	if err != nil {
		return nil, err
	}
	// Cheap guard before the expensive enumeration: a baselined store short-
	// circuits without touching the loader.
	if store.IsBaselined() {
		return &BaselineTrustResult{Status: "already"}, nil
	}

	loader := req.Loader
	if loader == nil {
		loader = bundleLoader(cfg)
	}

	grants, res := collectBaselineGrants(cfg, loader)
	if err := store.ApplyBaseline(grants, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return nil, err
	}
	res.Status = "baselined"
	return res, nil
}

// collectBaselineGrants enumerates every fragment, prompt, MCP server, and hook
// across all resolvable bundles and returns the trust grants for the present
// remote items, plus a tally. Builtin bundles are deliberately excluded: they are
// in-binary and never pass the resolver, so they are not part of the gate.
// Enumeration is fully fault-tolerant — a bundle that fails to load is warned
// and skipped, and a single item that fails to resolve/hash is warned and
// skipped — so one bad item never aborts the snapshot.
func collectBaselineGrants(cfg *config.Config, loader *bundles.Loader) ([]trust.Grant, *BaselineTrustResult) {
	res := &BaselineTrustResult{}
	infos, err := loader.List()
	if err != nil {
		// List is fault-tolerant and effectively cannot error today, but if it
		// ever does, an empty baseline is the safe outcome (everything gates).
		clidiag.Warn("ctxloom", "trust baseline: listing bundles failed, snapshot empty: %v", err)
		return nil, res
	}

	var grants []trust.Grant
	for _, info := range infos {
		bundle, err := loader.LoadFile(info.Path)
		if err != nil {
			clidiag.Warn("ctxloom", "trust baseline: skipping unloadable bundle %q: %v", info.Name, err)
			continue
		}
		for _, name := range bundle.FragmentNames() {
			grants = appendBaselineGrant(grants, res, cfg, loader, info.Name, trust.KindFragment, name)
		}
		for _, name := range bundle.PromptNames() {
			grants = appendBaselineGrant(grants, res, cfg, loader, info.Name, trust.KindPrompt, name)
		}
		for _, name := range bundle.MCPNames() {
			grants = appendBaselineGrant(grants, res, cfg, loader, info.Name, trust.KindMCP, name)
		}
		// Bundle hooks are an executable surface gated at their own choke (TR5);
		// baseline pre-existing remote ones so the rollout doesn't gate hooks that
		// already fire (TR6 follow-up #1). Identity = "<event>/<index>" (Entries()),
		// the same scheme config.extractHooksFromBundle gates on.
		for _, e := range bundle.Hooks.Entries() {
			grants = appendBaselineGrant(grants, res, cfg, loader, info.Name, trust.KindHook, e.ID())
		}
	}
	return grants, res
}

// appendBaselineGrant resolves one item, tallies it on res, and appends its
// grant when the item is present remote content. Local items (content or
// executable) are tallied but never granted; a resolve/hash failure is warned
// and tallied but never appended (it will gate later — the safe direction).
func appendBaselineGrant(grants []trust.Grant, res *BaselineTrustResult, cfg *config.Config, loader *bundles.Loader, source string, kind trust.ItemKind, name string) []trust.Grant {
	res.Total++
	ref := source + "#" + kind.Dir() + "/" + name
	tRef, loadRef, version, err := parseTrustItemRef(ref)
	if err != nil {
		clidiag.Warn("ctxloom", "trust baseline: skipping %q (will gate later): %v", ref, err)
		res.Failed++
		return grants
	}
	if tRef.IsLocal {
		// Local fragments/prompts are auto-allowed by the cascade and local
		// executables are never auto-trusted — neither is baselined.
		res.Local++
		return grants
	}
	hash, form, err := hashBaselineItem(cfg, loader, tRef, loadRef)
	if err != nil {
		clidiag.Warn("ctxloom", "trust baseline: skipping %q (will gate later): %v", ref, err)
		res.Failed++
		return grants
	}
	res.Granted++
	return append(grants, trust.Grant{
		RepoURL:     tRef.CanonicalURL(),
		Ref:         tRef.Key(),
		ContentHash: hash,
		Form:        string(form),
		SHAAtGrant:  version,
	})
}
