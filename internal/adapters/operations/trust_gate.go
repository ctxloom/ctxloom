package operations

import (
	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/composite"
	"github.com/ctxloom/ctxloom/internal/core/config"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// The gate every exposure and executable surface decides with is the
// generation's Trust (config.Config.ExecutableTrustGate, bound by the Owner
// from config.Sources.TrustPorts). Nothing here builds a second one: this
// file is the ADVISORY half — a withhold must never be silent or reasonless
// (docs/trust-model.md) — and the pipeline builders that pair the
// generation's gate with a reader.

// WarnWithheldBy surfaces one content-free advisory line PER item auth
// withheld — bundle MCP servers, hooks, prompt exports, fragments — naming the
// item and WHY (rejected, retracted by the publisher, pending review), via
// bundles.Reason.Explain. Purely advisory (fault tolerance); a no-op when auth
// is not a Trust's gate or withheld nothing.
func WarnWithheldBy(auth bundles.Authorizer) {
	for _, it := range composite.WithheldBy(auth) {
		clidiag.Warn("ctxloom", "withheld %s: %s", it.Ref, it.Verdict.Reason.Explain(it.Verdict.Detail))
	}
}

// warnWithheld is WarnWithheldBy for the gate an exposure pipeline decided
// with (exposurePipelineGated): nil for an injected test pipeline, which has
// no reasoned advisory to print.
func warnWithheld(gate bundles.Authorizer) {
	if gate == nil {
		return
	}
	WarnWithheldBy(gate)
}

// exposurePipeline returns the exposure PROCESS stage: one bundle reader — the
// same cfg.BundleLoader every management and listing path uses — wrapped
// in a pipeline that gates with the generation's Trust and serves the
// configured form. ONLY exposure surfaces build it (assembly, the ctxloom://
// fragment|prompt|skill resources, fragment-reading hooks, SessionStart regen);
// management/listing paths keep reading through the bare loader, which resolves
// pending content so a human can still review, accept or stamp it.
//
// "Is this exposure gated" is therefore a property of the pipeline, not of the
// reader — nothing about how the reader was CONSTRUCTED differs between the two.
//
// This shape serves an explicit by-name ask or a listing, never a run's
// assembly, so it does not consult link groups (bundles.LinksUnchecked): the
// caller named the item, and "what exists" must not shrink with a profile.
func exposurePipeline(cfg *config.Config) *bundles.Pipeline {
	pipe, _ := exposurePipelineGated(cfg, bundles.LinksUnchecked())
	return pipe
}

// exposurePipelineGated is exposurePipeline's sibling: it builds the identical
// gated exposure pipeline but ALSO returns the gate it decides with, so a
// caller that reports why items were withheld (warnWithheld) can read each
// withheld ref's full verdict instead of just a bare ref list.
//
// links is the run's link grant (cfg.LinkGrant over the profiles being
// assembled) for a surface that assembles a run, or bundles.LinksUnchecked
// for one that does not — stated by the caller, because only it knows which.
func exposurePipelineGated(cfg *config.Config, links bundles.LinkGrant) (*bundles.Pipeline, bundles.Authorizer) {
	gate := cfg.ExecutableTrustGate()
	return bundles.NewPipeline(cfg.BundleLoader(), gate, links, cfgPreferDistilled(cfg)), gate
}

// cfgPreferDistilled returns the caller's raw-vs-distilled form choice, nil-safe.
//
// Form selection is a PROCESS-stage decision (docs/design/engine-delivery-seam.
// design.md, "ALL processing lives in the middle"): the read stage — the bundle
// reader — carries no preference at all, so the pipeline that processes its
// output names the form. A nil cfg (an injected-pipeline test) gets the same
// default an unset setting does, which is distilled.
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
