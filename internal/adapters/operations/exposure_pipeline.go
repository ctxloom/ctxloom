package operations

import (
	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
)

// exposurePipeline returns the exposure PROCESS stage: one bundle reader — the
// same cfg.BundleLoader every management and listing path uses — wrapped in a
// pipeline that serves the configured form. ONLY exposure surfaces build it
// (assembly, the ctxloom:// fragment|prompt|skill resources, fragment-reading
// hooks, SessionStart regen).
//
// This shape serves an explicit by-name ask or a listing, never a run's
// assembly, so it does not consult link groups (bundles.LinksUnchecked): the
// caller named the item, and "what exists" must not shrink with a profile.
func exposurePipeline(cfg *config.Config) *bundles.Pipeline {
	return exposurePipelineWithLinks(cfg, bundles.LinksUnchecked())
}

// exposurePipelineWithLinks is exposurePipeline under a stated link grant:
// bundles.ServerGrant over a run's granted MCP set for a surface that
// assembles a run, or bundles.LinksUnchecked for one that does not — stated
// by the caller, because only it knows which.
func exposurePipelineWithLinks(cfg *config.Config, links bundles.LinkGrant) *bundles.Pipeline {
	return bundles.NewPipeline(cfg.BundleLoader(), links, cfgPreferDistilled(cfg))
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
