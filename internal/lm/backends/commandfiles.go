package backends

import (
	"strings"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/ctxloom/internal/shared/agent"
)

// buildExports is the shared export loop: names + content are engine-agnostic
// plumbing, and pick projects the prompt's per-engine LLM export config into
// the engine-specific fields (enablement, description, hints). Each engine's
// field mapping stays explicit in its own function below — only the loop is
// shared, so an engine gaining an export field touches one place.
func buildExports(prompts []*bundles.LoadedContent, pick func(*bundles.LoadedContent) agent.CommandExport) []agent.CommandExport {
	names := exportNames(prompts)
	out := make([]agent.CommandExport, 0, len(prompts))
	for _, p := range prompts {
		e := pick(p)
		e.Name = names[p.Name]
		e.Content = p.Content
		out = append(out, e)
	}
	return out
}

// claudeExports resolves the claude-code per-prompt LLM export config.
func claudeExports(prompts []*bundles.LoadedContent) []agent.CommandExport {
	return buildExports(prompts, func(p *bundles.LoadedContent) agent.CommandExport {
		cc := p.LLM.ClaudeCode
		return agent.CommandExport{
			Enabled:      cc.IsEnabled(),
			Description:  cc.Description,
			ArgumentHint: cc.ArgumentHint,
			AllowedTools: cc.AllowedTools,
			Model:        cc.Model,
		}
	})
}

// codexExports resolves the codex per-prompt LLM export config.
func codexExports(prompts []*bundles.LoadedContent) []agent.CommandExport {
	return buildExports(prompts, func(p *bundles.LoadedContent) agent.CommandExport {
		cx := p.LLM.Codex
		return agent.CommandExport{
			Enabled:      cx.IsEnabled(),
			Description:  cx.Description,
			ArgumentHint: cx.ArgumentHint,
		}
	})
}

// kiroExports resolves the kiro per-prompt LLM export config.
func kiroExports(prompts []*bundles.LoadedContent) []agent.CommandExport {
	return buildExports(prompts, func(p *bundles.LoadedContent) agent.CommandExport {
		k := p.LLM.Kiro
		return agent.CommandExport{Enabled: k.IsEnabled(), Description: k.Description}
	})
}

// opencodeExports resolves the opencode per-prompt LLM export config. opencode
// commands carry only a description in their frontmatter (the body is the
// template), so — like kiro — only enablement + description are mapped.
func opencodeExports(prompts []*bundles.LoadedContent) []agent.CommandExport {
	return buildExports(prompts, func(p *bundles.LoadedContent) agent.CommandExport {
		o := p.LLM.Opencode
		return agent.CommandExport{Enabled: o.IsEnabled(), Description: o.Description}
	})
}

// exportNames maps each prompt's full identity (LoadedContent.Name) to its
// export-facing command name. The short form (bundle's last path segment +
// item, see LoadedContent.ExportName) is used whenever it's unambiguous within
// the export set; when two bundles shorten to the same name, the colliders
// fall back to their full identity — sanitized for filesystem safety — so
// neither silently overwrites the other's command file.
func exportNames(prompts []*bundles.LoadedContent) map[string]string {
	counts := make(map[string]int, len(prompts))
	for _, p := range prompts {
		counts[p.ExportName()]++
	}
	names := make(map[string]string, len(prompts))
	for _, p := range prompts {
		short := p.ExportName()
		if counts[short] > 1 {
			names[p.Name] = sanitizeExportName(p.Name)
			continue
		}
		names[p.Name] = short
	}
	return names
}

// sanitizeExportName makes a collision-fallback name safe to use as a filename
// across platforms: ':' is invalid on Windows and '\' is a path separator
// there, so both collapse to '-'. The per-agent writers handle '/' themselves.
func sanitizeExportName(name string) string {
	return strings.NewReplacer(":", "-", `\`, "-").Replace(name)
}
