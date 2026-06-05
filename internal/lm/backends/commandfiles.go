package backends

import (
	"github.com/ctxloom/ctxloom/internal/agent"
	"github.com/ctxloom/ctxloom/internal/agent/claude"
	"github.com/ctxloom/ctxloom/internal/agent/gemini"
	"github.com/ctxloom/ctxloom/internal/bundles"
)

// WriteCommandFilesFor writes slash-command files for the named backend,
// dispatching to the per-agent writer. This is the cross-backend dispatch (like
// WriteSettings): it lives in the wiring layer because it maps ctxloom's bundle
// content to the agent-agnostic agent.CommandExport for the target backend —
// resolving that backend's enablement + metadata — so the per-agent writers
// (claude.WriteCommandFiles, gemini.WriteCommandFiles) never import bundles.
// Unsupported backends silently succeed.
func WriteCommandFilesFor(backendName, workDir string, prompts []*bundles.LoadedContent, opts ...CommandFileOption) error {
	switch backendName {
	case "claude-code":
		return claude.WriteCommandFiles(workDir, claudeExports(prompts), opts...)
	case "gemini":
		return gemini.WriteCommandFiles(workDir, geminiExports(prompts), opts...)
	default:
		return nil
	}
}

// claudeExports maps loaded bundle content to Claude command exports, resolving
// the claude-code per-prompt LLM export config.
func claudeExports(prompts []*bundles.LoadedContent) []agent.CommandExport {
	out := make([]agent.CommandExport, 0, len(prompts))
	for _, p := range prompts {
		cc := p.LLM.ClaudeCode
		out = append(out, agent.CommandExport{
			Name:         p.Name,
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

// geminiExports maps loaded bundle content to Gemini command exports, resolving
// the gemini per-prompt LLM export config.
func geminiExports(prompts []*bundles.LoadedContent) []agent.CommandExport {
	out := make([]agent.CommandExport, 0, len(prompts))
	for _, p := range prompts {
		g := p.LLM.Gemini
		out = append(out, agent.CommandExport{
			Name:        p.Name,
			Content:     p.Content,
			Enabled:     g.IsEnabled(),
			Description: g.Description,
		})
	}
	return out
}
