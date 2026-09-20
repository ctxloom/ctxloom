// Package claudeengine is claude-code's HOSTING record: what the backend
// registry still needs to run the engine kind claude.Build declares and the
// port does not yet carry — the backend constructor, the typed config, the
// named-form table, the settings writer, the hook scope guard, the version
// command and the retired-scraper reason — and the transcript readers the
// composition root hands the kind (claude.WithTranscripts). It is a
// subpackage rather than part of internal/engines/claude because the readers
// and the version command are adapters, and the lean parent package is
// linked by the ltk and taskloom binaries, which must not carry them.
//
// Nothing outside this package and internal/engines/claude names this
// engine; a fact about claude that some other package needs is declared on
// its kind or here and read back through the registry.
package claudeengine

import (
	"github.com/ctxloom/ctxloom/internal/adapters/engineversion"
	claudereader "github.com/ctxloom/ctxloom/internal/adapters/transcript/vendorreader/claude"
	"github.com/ctxloom/ctxloom/internal/core/agent"
	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
	"github.com/ctxloom/ctxloom/internal/lm/hosting"
)

// Transcripts are the readers of claude's own store, as engine.TranscriptReader
// values the composition root hands claude.Build.
func Transcripts() []engine.TranscriptReader {
	out := make([]engine.TranscriptReader, 0, len(claudereader.VersionedAdapters))
	for _, a := range claudereader.VersionedAdapters {
		out = append(out, a)
	}
	return out
}

// Hosting returns claude-code's hosting record: what the backend registry
// still needs to run the kind claude.Build declares. Every fact is built
// from the engine's own constants, so there is no second copy to drift.
func Hosting() hosting.Hosting {
	return hosting.Hosting{
		Engine: claude.EngineName,
		NewBackend: func(launch agent.Launcher) agent.Backend {
			b := claude.NewClaudeCode()
			b.SetLauncher(launch)
			return b
		},
		NewConfig:      func() agent.BackendConfig { return &claude.ClaudeConfig{} },
		Surfaces:       claude.Declaration(),
		SettingsWriter: engine.Provide(claude.NewWriter),
		// claude's project settings.json collapses onto its user-global one
		// exactly when workDir == $HOME — found live (`manage hooks install`
		// run from $HOME silently went global).
		HookGlobalScope: engine.Provide(hosting.HookGlobalScope{
			Paths: func(workDir string) (string, string, error) {
				global, err := claude.GlobalSettingsPath()
				return claude.ProjectSettingsPath(workDir), global, err
			},
			Label: "Claude Code's user-global settings file",
		}),
		VersionCommand: engine.Provide(engineversion.Command{Args: []string{"--version"}, Parse: parseVersion}),
		// The ~/.claude/projects/*.jsonl scraper was proven broken (wrong
		// filename) and deleted outright rather than demoted; canonical
		// capture, read back through the kind's Transcripts, is the only
		// source.
		NoLegacyHistoryReason: "claude's legacy session scraper was deleted; canonical capture is the only transcript source",
	}
}

// parseVersion reads `claude --version`. MEASURED: "2.1.225 (Claude Code)" —
// the version leads, the product name follows in parentheses.
func parseVersion(output string) (string, error) {
	return engineversion.TokenAt(output, 0)
}
