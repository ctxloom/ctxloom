package operations

import (
	"strings"

	"github.com/ctxloom/ctxloom/internal/core/bundles"
	"github.com/ctxloom/ctxloom/internal/core/config"
)

// ResolveSetupPrompt returns the setup prompt to emit: the supplied built-in
// guidance, PLUS every admitted companion's typed `init.setup_guidance`
// (bundles.InitLoadout), in companion-ref order for a deterministic, stable
// composition across runs. Nothing replaces anything: each contribution adds
// to the built-in, it never substitutes for it.
//
// Setup guidance IS an exposure surface — text reaching an agent with tool
// access at init, possibly unattended — so it is read through the gated
// pipeline (Pipeline.InitLoadouts), where a human's rejection of a companion
// beats the companion exemption. It never gates/writes on its own, and a nil
// config falls back to the built-in alone: setup must never be blocked by a
// companion.
func ResolveSetupPrompt(cfg *config.Config, builtin string) string {
	if cfg == nil {
		return builtin
	}
	loader := cfg.BundleLoader()
	if loader == nil {
		return builtin
	}
	// preferDistilled stays false: the INIT loadout has no distilled form.
	pipe := bundles.NewPipeline(loader, cfg.ExecutableTrustGate(), bundles.LinksUnchecked(), false)
	parts := []string{builtin}
	for _, admitted := range pipe.InitLoadouts() {
		if text := strings.TrimSpace(admitted.Init.SetupGuidance); text != "" {
			parts = append(parts, admitted.Init.SetupGuidance)
		}
	}
	if len(parts) == 1 {
		return builtin
	}
	return strings.Join(parts, "\n\n---\n\n")
}
