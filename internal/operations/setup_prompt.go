package operations

import (
	"strings"

	"github.com/ctxloom/ctxloom/internal/config"
)

// SetupPromptSkillName is the well-known skill name a bundle ships to OVERRIDE
// the built-in agent-assisted subagent-setup prompt. Shipping the onboarding /
// composition guidance as bundle content (data) — rather than baking it into the
// ctxloom binary — lets a remote (e.g. ctxloom-default) evolve or specialize the
// setup flow without a ctxloom release.
const SetupPromptSkillName = "subagent-setup"

// ResolveSetupPrompt returns the subagent-setup prompt to emit: a bundle-shipped
// `subagent-setup` skill's content when one is installed (a full override of the
// built-in), else the supplied built-in. Discovery is via the bundle loader
// directly (not the slash-command export path), so a designated setup skill is
// found regardless of its export enablement, and it never gates/writes — a nil
// config or any load failure falls back to the built-in and never blocks setup.
func ResolveSetupPrompt(cfg *config.Config, builtin string) string {
	if cfg == nil {
		return builtin
	}
	loader := cfg.SeededBundleLoader(false)
	if loader == nil {
		return builtin
	}
	infos, err := loader.ListAllSkills()
	if err != nil {
		return builtin
	}
	for _, info := range infos {
		if !setupSkillNameMatches(info.Name, SetupPromptSkillName) {
			continue
		}
		if content, gerr := loader.GetSkill(info.Name); gerr == nil && strings.TrimSpace(content.Content) != "" {
			return content.Content
		}
	}
	return builtin
}

// setupSkillNameMatches reports whether a skill's (possibly bundle-qualified)
// name refers to the bare skill `base` — matching "subagent-setup",
// "<bundle>#skills/subagent-setup", or "<path>/subagent-setup".
func setupSkillNameMatches(full, base string) bool {
	if full == base {
		return true
	}
	if i := strings.LastIndexAny(full, "/#"); i >= 0 {
		return full[i+1:] == base
	}
	return false
}
