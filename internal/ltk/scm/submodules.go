// Package scm reads the source-control metadata the hook needs to evaluate
// rules. Today that is just the set of git submodule paths declared in
// .gitmodules, used to expand a `path: ["@submodules"]` rule into a concrete
// directory pattern per submodule (see rules.Config.ExpandSubmodules).
package scm

import (
	"errors"
	"fmt"
	"io/fs"
	"path/filepath"
	"strings"

	"github.com/spf13/afero"
)

// SubmodulePaths returns the submodule paths declared in the nearest .gitmodules
// found at or above startDir (the repo root's, in practice). It returns nil,nil
// when there is no .gitmodules or it declares none, and an error when one EXISTS
// but could not be read — the caller must be able to tell "this repo has no
// submodules" from "I could not find out", because the two used to collapse into
// the same nil and left a `path: ["@submodules"]` rule silently guarding nothing.
// Paths are returned exactly as written — slash-separated and repo-relative.
//
// The walk stops at the first directory containing .git (file or dir): that is
// this repository's root, and a parent repository's .gitmodules describes the
// PARENT's submodules, whose repo-relative paths would expand into spurious
// rules inside the inner repo. (Same boundary as the config search.)
func SubmodulePaths(fsys afero.Fs, startDir string) ([]string, error) {
	dir := filepath.Clean(startDir)
	for {
		modules := filepath.Join(dir, ".gitmodules")
		data, err := afero.ReadFile(fsys, modules)
		if err == nil {
			return parseSubmodulePaths(string(data)), nil
		}
		// "Not here, keep walking" is the ONLY read failure that means anything
		// about submodules. Any other error (permissions, I/O, a directory where
		// the file should be) is us failing to find out — and it used to be
		// reported as the identical nil that "this repo has no submodules"
		// returns, which a `path: ["@submodules"]` rule turns into a rule with
		// zero patterns that guards nothing at all. Say which one it was.
		if !errors.Is(err, fs.ErrNotExist) {
			return nil, fmt.Errorf("read %s: %w", modules, err)
		}
		if ok, _ := afero.Exists(fsys, filepath.Join(dir, ".git")); ok {
			return nil, nil // repository root without a .gitmodules — do not leave the repo
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return nil, nil // reached the filesystem root without finding one
		}
		dir = parent
	}
}

// parseSubmodulePaths extracts the `submodule.<name>.path` values from a
// .gitmodules document. .gitmodules is git-config (INI) format, and a `path`
// key means "submodule path" ONLY inside a `[submodule …]` stanza — git itself
// reads no other section here. A stanza-blind scan would promote any other
// section's `path` key into a submodule, and `@submodules` expansion turns each
// one into a deny pattern, so the rule would guard directories the operator
// never declared.
//
// Section names are case-insensitive in git-config, and a variable may follow
// its section header on the same line ("[submodule \"x\"] path = x" is one
// legal stanza), so both forms are handled.
func parseSubmodulePaths(content string) []string {
	var paths []string
	inSubmodule := false
	for line := range strings.SplitSeq(content, "\n") {
		line = strings.TrimSpace(line)
		if name, rest, ok := sectionHeader(line); ok {
			inSubmodule = name == "submodule"
			line = rest
		}
		if !inSubmodule {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok || strings.TrimSpace(key) != "path" {
			continue
		}
		if p := strings.TrimSpace(val); p != "" {
			paths = append(paths, filepath.ToSlash(p))
		}
	}
	return paths
}

// sectionHeader splits a git-config section header off the front of line. It
// returns the lowercased section name (git-config section names are
// case-insensitive), whatever follows the closing bracket, and whether line
// started a header at all. Both subsection spellings are covered: the quoted
// `[submodule "name"]` form git writes, and the deprecated `[submodule.name]`
// dotted form.
func sectionHeader(line string) (name, rest string, ok bool) {
	if !strings.HasPrefix(line, "[") {
		return "", "", false
	}
	end := strings.IndexByte(line, ']')
	if end < 0 {
		return "", "", false
	}
	inner := line[1:end]
	if i := strings.IndexAny(inner, " \t."); i >= 0 {
		inner = inner[:i]
	}
	return strings.ToLower(strings.TrimSpace(inner)), strings.TrimSpace(line[end+1:]), true
}
