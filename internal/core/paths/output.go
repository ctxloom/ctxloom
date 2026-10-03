package paths

import (
	"bufio"
	"fmt"
	"path/filepath"
	"strings"

	harpid "github.com/ctxloom/ctxloom/internal/shared/harp"
)

// whatOutputBase names the default output base in a resolution failure.
const whatOutputBase = "the default output base"

// DefaultOutputBase returns <Documents>/ctxloom: where a session's readable
// outputs (essence, next step, plans, segment essences, published reports)
// go when the output_dir config key does not say otherwise. Documents is
// resolved per platform (documentsDir) because it is the folder a human
// already browses — and it is NOT under ~/.ctxloom, so a test binary
// resolving the real one is refused like any other home-rooted store.
func DefaultOutputBase() (string, error) {
	docs, err := documentsDir()
	if err != nil {
		return "", fmt.Errorf("resolve %s: %w", whatOutputBase, err)
	}
	base := filepath.Join(docs, OutputDirName)
	if err := UnsandboxedHomeError(whatOutputBase, base, "set HOME to a temp dir in the test, or configure output_dir"); err != nil {
		return "", err
	}
	return base, nil
}

// OutputDir returns <base>/<project>/<harp>: one session's output dir. The
// project is the PLAIN name of projectDir — two repositories with the same
// name share a project folder, and the harp keeps their sessions apart.
func OutputDir(base, projectDir, harp string) (string, error) {
	if base == "" {
		return "", fmt.Errorf("output dir for %q: no output base", harp)
	}
	if err := harpid.Validate(harp); err != nil {
		return "", err
	}
	project := filepath.Base(filepath.Clean(projectDir))
	if projectDir == "" || project == "." || project == ".." || project == string(filepath.Separator) || strings.ContainsAny(project, `/\`) {
		return "", fmt.Errorf("output dir for %q: project dir %q has no name to file it under", harp, projectDir)
	}
	return filepath.Join(base, project, harp), nil
}

// userDirsDocumentsKey is the user-dirs.dirs variable naming the Documents
// folder (freedesktop xdg-user-dirs).
const userDirsDocumentsKey = "XDG_DOCUMENTS_DIR"

// parseUserDirsDocuments reads XDG_DOCUMENTS_DIR out of a user-dirs.dirs
// file. The format allows exactly two value shapes, both double-quoted:
// "$HOME/<rel>" and an absolute path. "$HOME/" alone is how the format
// DISABLES a folder, so it is reported absent, as is any other shape.
func parseUserDirsDocuments(content, home string) (string, bool) {
	sc := bufio.NewScanner(strings.NewReader(content))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		val, found := strings.CutPrefix(line, userDirsDocumentsKey+"=")
		if !found {
			continue
		}
		if len(val) < 2 || val[0] != '"' || val[len(val)-1] != '"' {
			return "", false
		}
		val = val[1 : len(val)-1]
		if rel, ok := strings.CutPrefix(val, "$HOME/"); ok {
			if rel == "" {
				return "", false
			}
			return filepath.Join(home, filepath.FromSlash(rel)), true
		}
		if filepath.IsAbs(val) {
			return filepath.Clean(val), true
		}
		return "", false
	}
	return "", false
}
