package operations

import (
	"sort"
	"strings"

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
// found across cfg.SeededBundleLoader — a repo bundle's and an installed
// companion's loadout both land in that same seeded set (config.go's
// SeededBundleLoader merges loadRemoteBundleSeed and companionBundleSeed), so
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
	loader := cfg.SeededBundleLoader()
	if loader == nil {
		return builtin
	}
	infos, err := loader.ListAllCommands()
	if err != nil {
		// This used to fall back to the built-in with the error discarded —
		// an installed bundle's setup guidance silently vanishing looked
		// identical to nobody having shipped any. The fallback is still
		// correct (setup must never be blocked by a listing failure); only
		// the silence was the bug.
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
		content, gerr := loader.GetCommand(ref, false)
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
