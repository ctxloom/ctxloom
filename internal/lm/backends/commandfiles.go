package backends

import (
	"strings"

	"github.com/ctxloom/ctxloom/internal/bundles"
	"github.com/ctxloom/shared/agent"
)

// WriteCommandFilesFor writes slash-command files for the named backend,
// dispatching to the per-agent writer via the descriptor table (registry.go).
// This is the cross-backend dispatch (like WriteSettings): it lives in the
// wiring layer because it maps ctxloom's bundle content to the agent-agnostic
// agent.CommandExport for the target backend — resolving that backend's
// enablement + metadata — so the per-agent writers (claude.WriteCommandFiles,
// antigravity.WriteCommandFiles) never import bundles. Unsupported backends
// silently succeed.
func WriteCommandFilesFor(backendName, workDir string, prompts []*bundles.LoadedContent, opts ...agent.CommandFileOption) error {
	d, ok := descriptors[backendName]
	if !ok || d.exports == nil || d.writeCommands == nil {
		return nil
	}
	return d.writeCommands(workDir, d.exports(prompts), opts...)
}

// claudeExports maps loaded bundle content to Claude command exports, resolving
// the claude-code per-prompt LLM export config.
func claudeExports(prompts []*bundles.LoadedContent) []agent.CommandExport {
	names := exportNames(prompts)
	out := make([]agent.CommandExport, 0, len(prompts))
	for _, p := range prompts {
		cc := p.LLM.ClaudeCode
		out = append(out, agent.CommandExport{
			Name:         names[p.Name],
			Content:      p.Content,
			Enabled:      cc.IsEnabled(),
			Description:  cc.Description,
			ArgumentHint: cc.ArgumentHint,
			AllowedTools: cc.AllowedTools,
			Model:        cc.Model,
		})
	}
	return out
}

// antigravityExports maps loaded bundle content to Antigravity command
// exports, resolving the antigravity per-prompt LLM export config.
func antigravityExports(prompts []*bundles.LoadedContent) []agent.CommandExport {
	names := exportNames(prompts)
	out := make([]agent.CommandExport, 0, len(prompts))
	for _, p := range prompts {
		a := p.LLM.Antigravity
		out = append(out, agent.CommandExport{
			Name:        names[p.Name],
			Content:     p.Content,
			Enabled:     a.IsEnabled(),
			Description: a.Description,
		})
	}
	return out
}

// codexExports maps loaded bundle content to Codex command exports, resolving
// the codex per-prompt LLM export config.
func codexExports(prompts []*bundles.LoadedContent) []agent.CommandExport {
	names := exportNames(prompts)
	out := make([]agent.CommandExport, 0, len(prompts))
	for _, p := range prompts {
		cx := p.LLM.Codex
		out = append(out, agent.CommandExport{
			Name:         names[p.Name],
			Content:      p.Content,
			Enabled:      cx.IsEnabled(),
			Description:  cx.Description,
			ArgumentHint: cx.ArgumentHint,
		})
	}
	return out
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
