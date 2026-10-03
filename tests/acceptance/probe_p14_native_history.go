// P14, NATIVE HISTORY (capability_native_history.feature): the vendor facts
// the session layout's native-history mechanism stands on. ctxloom keeps an
// agent's claude conversation history in sessions/<harp>/native/claude/ and
// reaches it through a RELATIVE symlink at the config home's projects/, so that
// deleting the disposable config home on Close never deletes the history, and
// so the same relative link resolves inside a container that mounts native/.
// That design holds only while claude
//
//   - writes its conversation .jsonl under $CLAUDE_CONFIG_DIR/projects/ when it
//     runs in ctxloom's container (the container-writes cell), and
//   - writes THROUGH a symlinked projects/ and never replaces the link with a
//     real directory (the symlinked-projects cell).
//
// If a claude release breaks either, history silently lands in the config
// home and is deleted with it on Close — so these are re-run on a pin bump.
//
// UNTAGGED, like probe_p12_permission_hook.go: the judges are pure functions
// a hermetic test reaches.
package acceptance

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/ctxloom/ctxloom/internal/engines/claude"
)

const p14Family = "P14 native history"

// p14 variants: the two vendor facts, one cell each.
const (
	p14ContainerWrites   = "container-writes"
	p14SymlinkedProjects = "symlinked-projects"
)

// p14NativeLink is the link the symlinked-projects cell plants at
// <cfg>/projects: the SAME relative shape the session layout uses
// (home/claude/projects -> ../../native/claude/projects), so the cell proves
// the shape production depends on, not merely "some symlink".
var p14NativeLink = filepath.Join("..", "..", "native", claude.HomeLeaf, claude.TranscriptsDirName)

// errP14NoHistory is a run that left no conversation .jsonl where the cell
// looked: the engine wrote its history somewhere else, or not at all.
var errP14NoHistory = errors.New("no conversation .jsonl")

// errP14LinkReplaced is the failure the native-history design cannot absorb:
// claude swapped the planted projects/ link for a real directory.
var errP14LinkReplaced = errors.New("claude REPLACED the projects/ symlink with a real entry")

// p14JudgeContainerWrites passes when the session's native history, as
// observed on the host during the run (rel paths under native/), holds a
// conversation .jsonl under claude's projects/ — written in the container
// through the engine home's link.
func p14JudgeContainerWrites(configHome string, configTree []string) error {
	prefix := filepath.Join(claude.HomeLeaf, claude.TranscriptsDirName) + string(filepath.Separator)
	for _, rel := range configTree {
		if strings.HasPrefix(rel, prefix) && strings.HasSuffix(rel, ".jsonl") {
			return nil
		}
	}
	return fmt.Errorf("%s %s: %w under %s in the session's native history %s (seen: %v)",
		p14Family, p14ContainerWrites, errP14NoHistory, prefix, configHome, configTree)
}

// p14JudgeSymlink passes when <cfg>/projects is STILL the symlink the cell
// planted (same target text) and the link's target holds a conversation
// .jsonl, i.e. claude wrote through the link.
func p14JudgeSymlink(cfg string) error {
	link := filepath.Join(cfg, claude.TranscriptsDirName)
	st, err := os.Lstat(link)
	if err != nil {
		return fmt.Errorf("%s %s: %s is gone after the run: %w", p14Family, p14SymlinkedProjects, link, err)
	}
	if st.Mode()&fs.ModeSymlink == 0 {
		return fmt.Errorf("%s %s: %w (%s is now a %v) — history written there is deleted with the config home", p14Family, p14SymlinkedProjects, errP14LinkReplaced, link, st.Mode().Type())
	}
	got, err := os.Readlink(link)
	if err != nil {
		return fmt.Errorf("%s %s: reading %s: %w", p14Family, p14SymlinkedProjects, link, err)
	}
	if got != p14NativeLink {
		return fmt.Errorf("%s %s: %s now points at %q, planted %q", p14Family, p14SymlinkedProjects, link, got, p14NativeLink)
	}
	target := filepath.Join(cfg, p14NativeLink)
	found := false
	_ = filepath.WalkDir(target, func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".jsonl") {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	if !found {
		return fmt.Errorf("%s %s: %w under the link's target %s", p14Family, p14SymlinkedProjects, errP14NoHistory, target)
	}
	return nil
}
