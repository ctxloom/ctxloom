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

// parseSubmodulePaths extracts the `path = …` values from a .gitmodules document.
// .gitmodules is git-config (INI) format; each [submodule "name"] stanza carries
// a `path` key. We read only those keys, which is all the rule expansion needs.
func parseSubmodulePaths(content string) []string {
	var paths []string
	for line := range strings.SplitSeq(content, "\n") {
		line = strings.TrimSpace(line)
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
