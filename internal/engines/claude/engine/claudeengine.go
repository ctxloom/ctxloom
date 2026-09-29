// Package claudeengine is what the composition root hands claude.Build that
// the lean parent package must not import: the transcript readers of
// claude's own store and the reading of `claude --version`, both adapters.
// It is a subpackage rather than part of internal/engines/claude because
// the ltk and taskloom binaries link the parent and must not carry them.
//
// Nothing outside this package and internal/engines/claude names this
// engine; a fact about claude that some other package needs is declared on
// its kind and read back through the registry.
package claudeengine

import (
	"github.com/ctxloom/ctxloom/internal/adapters/engineversion"
	claudereader "github.com/ctxloom/ctxloom/internal/adapters/transcript/vendorreader/claude"
	"github.com/ctxloom/ctxloom/internal/core/engine"
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

// Version is how `claude --version` is read. MEASURED: "2.1.225 (Claude
// Code)" — the version leads, the product name follows in parentheses. The
// floor is the release the headless route was verified live on
// (--permission-prompts none, system/permission_denied, result
// permission_denials).
func Version() engine.VersionCommand {
	return engine.VersionCommand{
		Args:  []string{"--version"},
		Parse: func(output string) (string, error) { return engineversion.TokenAt(output, 0) },
		Floor: "2.1.283",
	}
}
