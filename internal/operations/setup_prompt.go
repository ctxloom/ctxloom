package operations

import (
	"sort"
	"strings"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// SetupPromptCommandName is the well-known command name a bundle ships to
// AUGMENT the built-in agent-assisted agent-setup prompt. Shipping the
// onboarding / composition guidance as bundle content (data) — rather than
// baking it into the ctxloom binary — lets a remote (e.g. ctxloom-default), a
// personal repo, or an installed companion evolve or add to the setup flow
// without a ctxloom release. Nothing replaces anything: every matching
// command's content adds to the built-in, it never substitutes for it.
const SetupPromptCommandName = "agent-setup"

// ResolveSetupPrompt returns the agent-setup prompt to emit: the supplied
// built-in guidance, PLUS the content of every installed `agent-setup` command
// found across cfg.BundleLoader — a repo bundle's and an installed
// companion's loadout both land in that same seeded set (config.go's
// the config bundle loader composes the project, remote and companion readers), so
// composing over ListAllCommands picks up both sources with no separate
// companion-specific lookup. Contributions are sorted by command name for a
// deterministic, stable order across runs. Discovery is via the bundle loader
// directly (not the slash-command export path), so a designated setup command
// is found regardless of its export enablement, and it never gates/writes — a
// nil config or any load failure falls back to the built-in alone and never
// blocks setup.
func ResolveSetupPrompt(cfg *config.Config, builtin string) string {
	if cfg == nil {
		return builtin
	}
	loader := cfg.BundleLoader()
	if loader == nil {
		return builtin
	}
	// Agent-setup guidance IS an exposure surface, and used not to be gated.
	// It was the one bundle path building its pipeline with AdmitAll while
	// hooks, MCP servers, skills and tooling all ran through the trust gate --
	// so text from an unreviewed remote bundle reached the engine at init
	// without anyone having looked at it. Trusted-content-reaching-an-agent is
	// an execution vector here: agents run with tool access, and the unattended
	// protocol runs them with no human present.
	//
	// The old rationale was that init runs at PermissionDefault inside the
	// vendor TUI, making the engine's own approval prompts the consent surface.
	// That is real but CONDITIONAL: discoveryPermissionMode honours a project's
	// declared top-level permissions and only falls back to PermissionDefault,
	// so a project could lose the mitigation silently.
	//
	// Gating costs little, because the decision function admits local, builtin
	// and trusted-signer content outright (see trust.Ref.IsLocal and
	// EffectiveTrust's SourceTrustedSigner tier). A project's OWN bundles and
	// anything signed by a key the user already trusts still contribute; only
	// an unreviewed REMOTE bundle is withheld, which is the same deal every
	// sibling surface already gets.
	//
	// preferDistilled stays false: this changes WHO is admitted, not which
	// bytes an admitted command contributes.
	pipe := bundles.NewPipeline(loader, buildContentGate(cfg, nil, cfgFS(cfg)), false)
	infos, err := loader.ListAllCommands()
	if err != nil {
		// Falling back to the built-in prompt on a listing failure is correct
		// — setup must never be blocked by it — but doing so silently would
		// make an installed bundle's setup guidance look identical to nobody
		// having shipped any, so the fallback warns instead of staying quiet.
		clidiag.Warn("ctxloom", "agent-setup: list installed commands: %v (using the built-in prompt alone)", err)
		return builtin
	}
	// Multiple installed bundles/loadouts can each ship an "agent-setup" command
	// (ContentInfo.Name is the bare command name, not bundle-qualified — see
	// ListAllCommands), so every match is addressed by its OWN containing bundle
	// via the explicit "<bundle>#commands/<name>" ref: GetCommand on a bare name
	// resolves ambiguously to whichever bundle it finds first (searchCommand),
	// which would silently drop every match but one.
	var refs []string
	for _, info := range infos {
		if setupCommandNameMatches(info.Name, SetupPromptCommandName) {
			refs = append(refs, info.Bundle+"#commands/"+info.Name)
		}
	}
	sort.Strings(refs)

	parts := []string{builtin}
	for _, ref := range refs {
		content, gerr := pipe.GetCommand(ref)
		if gerr != nil {
			// A per-ref GetCommand failure (e.g. the bundle vanished or
			// became unreadable between the listing above and this read)
			// used to drop that bundle's contribution with no signal at all.
			clidiag.Warn("ctxloom", "agent-setup: read %s: %v (skipping this contribution)", ref, gerr)
			continue
		}
		if strings.TrimSpace(content.Content) != "" {
			parts = append(parts, content.Content)
		}
	}
	if len(parts) == 1 {
		return builtin
	}
	return strings.Join(parts, "\n\n---\n\n")
}

// setupCommandNameMatches reports whether a command's (possibly
// bundle-qualified) name refers to the bare command `base` — matching
// "agent-setup", "<bundle>#commands/agent-setup", or "<path>/agent-setup".
func setupCommandNameMatches(full, base string) bool {
	if full == base {
		return true
	}
	if i := strings.LastIndexAny(full, "/#"); i >= 0 {
		return full[i+1:] == base
	}
	return false
}
