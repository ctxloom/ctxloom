package backends

import "github.com/ctxloom/ctxloom/internal/bundles"

// WriteCommandFilesFor writes slash-command files for the named backend,
// dispatching to the per-agent writer. This is the cross-backend dispatch (like
// WriteSettings): it lives in the wiring layer because it must reach both the
// claude writer (agent.WriteCommandFiles) and the gemini writer
// (WriteGeminiCommandFiles). Unsupported backends silently succeed.
func WriteCommandFilesFor(backendName, workDir string, prompts []*bundles.LoadedContent, opts ...CommandFileOption) error {
	switch backendName {
	case "claude-code":
		return WriteCommandFiles(workDir, prompts, opts...)
	case "gemini":
		return WriteGeminiCommandFiles(workDir, prompts, opts...)
	default:
		return nil
	}
}
