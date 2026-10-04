package paths

import (
	"fmt"
	"path/filepath"
	"strings"

	harpid "github.com/ctxloom/ctxloom/internal/shared/harp"
)

// whatOutputBase names the default output base in a resolution failure.
const whatOutputBase = "the default output base"

// DefaultOutputBase returns <documents>/ctxloom: where a session's readable
// outputs (essence, next step, plans, segment essences, published reports)
// go when the output_dir config key does not say otherwise. documents is the
// platform's Documents folder (platform.UserDirs), because it is the folder a
// human already browses — and it is NOT under ~/.ctxloom, so a test binary
// resolving the real one is refused like any other home-rooted store.
func DefaultOutputBase(documents string) (string, error) {
	if documents == "" {
		return "", fmt.Errorf("resolve %s: no Documents folder", whatOutputBase)
	}
	base := filepath.Join(documents, OutputDirName)
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
