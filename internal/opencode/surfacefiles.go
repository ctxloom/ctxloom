package opencode

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/shared/agent"
	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// ConfigFileName is the project-local config opencode reads (and strictly
// validates) from its cwd. ctxloom merges its managed keys into it
// (settings.go). Exported (renamed from the package-private
// opencodeConfigFile) so tests/arch's engine-layout gate can check
// internal/gitignore's "opencode.json" WorktreeArtifactPatterns entry
// against this package's own fact.
const ConfigFileName = "opencode.json"

// warnRevertFailure emits a diagnostic when an unwind step (reverting the
// opencode.json overlay, the context file, or the command/skill surfaces)
// itself fails while launchInteractive is already handling an earlier error.
// The earlier error is still what's returned — masking it with this one would
// be worse — but silently discarding the revert failure left the user's
// project holding whatever partial overlay state (e.g. a plan run's read-only
// permission block) with no warning at all.
func warnRevertFailure(what string, err error) {
	if err != nil {
		clidiag.Warn("ctxloom", "opencode: reverting %s failed after an earlier error; it may still be left modified: %v", what, err)
	}
}

// materializeContextSurface writes the assembled context to opencode's
// ctxloom-owned instruction file and returns the revert that removes it again.
// An empty context writes nothing and reverts to a no-op, so a run with no
// context never points opencode at an empty instruction file.
func materializeContextSurface(fs afero.Fs, workDir, context string) (func() error, error) {
	noop := func() error { return nil }
	// TrimSpace, not == "": a context of "\n" or "  " is not a context. Testing
	// for the empty string let 1-4 bytes of whitespace through, writing a
	// ctxloom-context.md that says nothing and pointing opencode's
	// instructions[] at it — a delivery indistinguishable from a real one.
	if strings.TrimSpace(context) == "" {
		return noop, nil
	}
	ctxPath := filepath.Join(workDir, opencodeContextFile)
	if err := fs.MkdirAll(filepath.Dir(ctxPath), 0o755); err != nil {
		return noop, fmt.Errorf("create .opencode directory: %w", err)
	}
	if err := agent.AtomicWriteFile(fs, ctxPath, []byte(context+"\n"), "ctxloom-context.md"); err != nil {
		return noop, err
	}
	return func() error {
		if e, _ := afero.Exists(fs, ctxPath); e {
			return fs.Remove(ctxPath)
		}
		return nil
	}, nil
}
