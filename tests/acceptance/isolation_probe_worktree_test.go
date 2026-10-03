//go:build acceptance

package acceptance

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/core/paths"
	"github.com/ctxloom/ctxloom/internal/engines/claude"
)

// The engine's config home on every cell is the session's engine home, not a
// worktree scratch dir: the watcher must see what lands there.
func TestScanScratchOnce_SeesTheSessionEngineHome(t *testing.T) {
	sessions := t.TempDir()
	transcript := filepath.Join(claude.HomeLeaf, claude.TranscriptsDirName, "proj", "s.jsonl")
	full := filepath.Join(sessions, "some-harp", paths.SessionEngineHomesDirName, transcript)
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(full, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	snap := scanScratchOnce(sessions)
	if !slices.Contains(snap.ConfigTree, transcript) {
		t.Fatalf("config tree %v does not carry the engine's transcript %s", snap.ConfigTree, transcript)
	}
}

// ctxloom itself writes into the engine home before launch (the instance
// config, the settings), so a home holding only those proves nothing about
// where the ENGINE wrote. Check (c) asks for the engine's own transcript.
func TestAssertProbeWorktree_ConfigHomeEvidenceMustBeTheEnginesOwnWrite(t *testing.T) {
	base := probeResult{Engine: "claude-code", Token: "TOK", Scratch: probeScratchSnapshot{TokenFound: true, TokenContent: "TOK"}}

	ctxloomOnly := base
	ctxloomOnly.Scratch.ConfigTree = []string{
		filepath.Join(claude.HomeLeaf, ".claude.json"),
		filepath.Join(claude.HomeLeaf, "settings.json"),
	}
	err := assertProbeWorktree(&ctxloomOnly)
	if err == nil || !strings.HasPrefix(err.Error(), "(c)") {
		t.Fatalf("a home holding only ctxloom's writes = %v, want a (c) failure", err)
	}

	engineWrote := base
	engineWrote.Scratch.ConfigTree = append(slices.Clone(ctxloomOnly.Scratch.ConfigTree),
		filepath.Join(claude.HomeLeaf, claude.TranscriptsDirName, "proj", "s.jsonl"))
	if err := assertProbeWorktree(&engineWrote); err != nil {
		t.Fatalf("a home carrying the engine's transcript = %v, want nil", err)
	}
}
