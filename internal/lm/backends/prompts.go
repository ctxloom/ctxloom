package backends

import (
	"strings"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/resources"
	"github.com/ctxloom/shared/clidiag"
)

// LoadPromptExports loads every prompt that exports as a slash command:
// ctxloom's embedded builtin commands plus every bundle prompt. This is the
// SINGLE prompt-export assembly — both the `ctxloom run` setup payload
// (AssembleManagedConfig) and operations.ApplyHooks route through it. The
// settings/command writers reconcile by removing all ctxloom-managed files and
// re-adding the assembled set, so two diverging assemblies would silently
// delete whatever one produced and the other didn't.
//
// The SeededBundleLoader is the only loader that also surfaces remote bundles
// from the lockfile clone cache; empty fs bundle dirs are fine — remote-only
// setups still produce commands.
func LoadPromptExports(cfg *config.Config, opts ...bundles.LoaderOption) []*bundles.LoadedContent {
	prompts := builtinPrompts()

	// Gate prompt command-file exports through the same per-item trust cascade as
	// content (trust rework, TR5 follow-up #2): a prompt whose effective content
	// the cascade denies must not be exported as a slash command either. The gate
	// is the cfg-injected executable gate (nil on management paths = no gating);
	// it keys on "<bundle>#prompts/<name>", identical to the content choke, so a
	// baselined/granted prompt is exported and an untrusted one is withheld.
	if cfg != nil {
		if gate := cfg.ExecutableTrustGate(); gate != nil {
			opts = append(opts, bundles.WithTrustGate(gate))
		}
	}

	loader := cfg.SeededBundleLoader(cfg.ShouldUseDistilled(), opts...)
	infos, err := loader.ListAllPrompts()
	if err != nil {
		// The reconciling writers remove-all-then-re-add, so a transient load
		// failure silently deletes the user's installed slash commands for
		// this run unless it is at least surfaced.
		clidiag.Warn("ctxloom", "bundle prompts unavailable; slash commands limited to builtins: %v", err)
		return prompts
	}
	for _, info := range infos {
		content, err := loader.GetPrompt(info.Name)
		if err != nil {
			clidiag.Warn("ctxloom", "skipping prompt %q: %v", info.Name, err)
			continue
		}
		prompts = append(prompts, content)
	}
	return prompts
}

// builtinPrompts returns ctxloom's built-in slash command prompts. These are
// embedded in the ctxloom binary and always available.
func builtinPrompts() []*bundles.LoadedContent {
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
