package delivery

import (
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/spf13/afero"

	"github.com/ctxloom/ctxloom/internal/core/present"
)

// ProjectClaims is the record's account of what any project writer (the
// legacy tag or the per-engine, per-kind family: Writer.Project) has
// installed in a file: the places it claims that the file holds now. It is
// what a settings status reads (the agent.SettingsOptions field of the same
// name).
func ProjectClaims(fs afero.Fs, records Ownership) func(target string) ([]string, error) {
	return func(target string) ([]string, error) {
		places, err := records.Paths(fs, target)
		if err != nil {
			return nil, err
		}
		var out []string
		for _, p := range places {
			if p.Live && slices.ContainsFunc(p.Writers, Writer.Project) {
				out = append(out, p.Pointer)
			}
		}
		return out, nil
	}
}

// ProjectContextClaims lists, sorted, the files under projectRoot that any
// at-rest context writer (a "project:<engine>:context" tag) claims a value
// in: materialized context a `ctxloom run` session in that project would
// meet beside its own (R5). The legacy bare "project" tag names no kind and
// is not counted; a session's own claims never are.
func ProjectContextClaims(records Ownership, projectRoot string) ([]string, error) {
	writers, err := records.Writers()
	if err != nil {
		return nil, err
	}
	seen := map[string]bool{}
	for _, w := range writers {
		if !w.Project() || !strings.HasSuffix(string(w), ":"+present.Context.String()) {
			continue
		}
		targets, err := records.Targets(w)
		if err != nil {
			return nil, err
		}
		for _, path := range targets {
			if under(projectRoot, path) {
				seen[path] = true
			}
		}
	}
	return slices.Sorted(maps.Keys(seen)), nil
}

// under reports whether path lies beneath root.
func under(root, path string) bool {
	rel, err := filepath.Rel(root, path)
	return err == nil && !filepath.IsAbs(rel) && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}
