package backends

import (
	"strings"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/resources"
	"github.com/ctxloom/shared/clidiag"
)

// LoadSkillExports loads every prompt that exports as a slash command for the
// SELECTED profiles: ctxloom's embedded builtin commands (always-on substrate)
// plus the prompts shipped by the selected profiles' bundles. This is the SINGLE
// prompt-export assembly — both the `ctxloom run` setup payload
// (AssembleManagedConfig, which passes the run's resolved profile set) and
// operations.ApplyHooks (which passes nil → the configured defaults) route
// through it. The settings/command writers reconcile by removing all
// ctxloom-managed files and re-adding the assembled set, so two diverging
// assemblies would silently delete whatever one produced and the other didn't.
//
// Bundle prompts are profile-scoped (config.ResolveBundleSkills) rather than a
// global ListAllSkills sweep, so a session only carries the skills its
// profile pulls in — not every prompt in every pulled bundle.
func LoadSkillExports(cfg *config.Config, profileNames []string, opts ...bundles.LoaderOption) []*bundles.LoadedContent {
	prompts := builtinSkills()
	prompts = append(prompts, cfg.ResolveBundleSkills(profileNames, opts...)...)
	return prompts
}

// builtinSkills returns ctxloom's built-in slash command prompts. These are
// embedded in the ctxloom binary and always available.
func builtinSkills() []*bundles.LoadedContent {
	names, err := resources.ListBuiltinCommands()
	if err != nil {
		clidiag.Warn("ctxloom", "builtin commands unavailable: %v", err)
		return nil
	}

	var prompts []*bundles.LoadedContent
	for _, name := range names {
		content, err := resources.GetBuiltinCommand(name)
		if err != nil {
			continue
		}

		description, body := parseMarkdownFrontmatter(string(content))
		prompts = append(prompts, &bundles.LoadedContent{
			Name:    name,
			Content: body,
			LLM: bundles.LLMExports{
				ClaudeCode: bundles.ClaudeCodeConfig{
					Description: description,
				},
				Antigravity: bundles.AntigravityConfig{
					Description: description,
				},
				Codex: bundles.CodexConfig{
					Description: description,
				},
			},
		})
	}
	return prompts
}

// parseMarkdownFrontmatter extracts description from YAML frontmatter and returns body.
// Expects format: ---\ndescription: ...\n---\nbody
func parseMarkdownFrontmatter(content string) (description, body string) {
	if !strings.HasPrefix(content, "---\n") {
		return "", content
	}

	// Find the closing ---
	rest := content[4:] // Skip opening "---\n"
	endIdx := strings.Index(rest, "\n---")
	if endIdx == -1 {
		return "", content
	}

	frontmatter := rest[:endIdx]
	body = strings.TrimPrefix(rest[endIdx+4:], "\n")

	// Parse description from frontmatter (simple key: value parsing)
	for _, line := range strings.Split(frontmatter, "\n") {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "description:") {
			description = strings.TrimSpace(strings.TrimPrefix(line, "description:"))
			// Remove surrounding quotes if present
			if len(description) >= 2 && description[0] == '"' && description[len(description)-1] == '"' {
				description = description[1 : len(description)-1]
			}
			break
		}
	}

	return description, body
}
