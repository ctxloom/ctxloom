package plans

import (
	"path/filepath"

	"github.com/ctxloom/ctxloom/internal/sessions"
)

// ProjectIndex is the harp → project-directory join table read from
// ~/.ctxloom/sessions/index.yaml. Plans live under ~/.ctxloom/sessions/<harp>/
// and carry no project of their own, so the owning session's index entry is the
// only thing that attributes a plan to a project.
//
// Paths are stored filepath.Clean'd so a cosmetic difference (trailing
// separator, "." segment) between the index and a caller's working directory
// can't make a plan vanish from its own project's listing.
type ProjectIndex map[string]string

// LoadProjectIndex reads the session index and returns the harp → project-dir
// table. Sessions with no recorded project dir are simply absent, which reads
// as "unattributable" at lookup time.
//
// The error is returned rather than folded into a bool at each lookup on
// purpose: an unreadable or corrupt index is a real failure, and a caller that
// quietly listed nothing because the index wouldn't parse is the silent-no-op
// this package must not commit.
func LoadProjectIndex() (ProjectIndex, error) {
	m, err := sessions.Open("")
	if err != nil {
		return nil, err
	}
	idx, err := m.Load()
	if err != nil {
		return nil, err
	}
	out := make(ProjectIndex, len(idx.Sessions))
	for _, e := range idx.Sessions {
		if e.HarpName == "" || e.ProjectDir == "" {
			continue
		}
		out[e.HarpName] = filepath.Clean(e.ProjectDir)
	}
	return out, nil
}

// ProjectDirOf returns the project directory the plan's owning session ran in.
// ok is false when that session has no index entry, or has one with no project
// dir — an ephemeral or worktree session, a pruned entry, or a hand-created
// plan file. Such a plan is UNATTRIBUTABLE, never "not yours": callers must
// still show it (marked), because a plan that silently disappears from every
// listing is indistinguishable from a plan that was lost.
func (px ProjectIndex) ProjectDirOf(p Plan) (projectDir string, ok bool) {
	dir, ok := px[p.Session]
	return dir, ok
}

// ListHomeScoped lists ~/.ctxloom/sessions plans split by project attribution:
// matched holds the plans whose session ran in projectDir (each with ProjectDir
// filled in), unattributed holds the plans whose session can't be attributed to
// any project at all.
//
// The split is two return values rather than one pre-filtered slice so the
// caller cannot accidentally drop the unattributed set — it has to decide what
// to do with it. Plans belonging to a DIFFERENT project are the only ones
// actually excluded.
func ListHomeScoped(projectDir string) (matched, unattributed []Plan, err error) {
	all, err := ListHome()
	if err != nil {
		return nil, nil, err
	}
	px, err := LoadProjectIndex()
	if err != nil {
		return nil, nil, err
	}
	want := filepath.Clean(projectDir)
	matched, unattributed = []Plan{}, []Plan{}
	for _, p := range all {
		dir, ok := px.ProjectDirOf(p)
		if !ok {
			unattributed = append(unattributed, p)
			continue
		}
		if dir != want {
			continue
		}
		p.ProjectDir = dir
		matched = append(matched, p)
	}
	return matched, unattributed, nil
}

// AttributeAll fills in ProjectDir on every plan it can attribute, leaving the
// rest empty. It is the --global counterpart to ListHomeScoped: nothing is
// filtered out, but the listing can still show which project each plan is from.
func AttributeAll(all []Plan) ([]Plan, error) {
	px, err := LoadProjectIndex()
	if err != nil {
		return nil, err
	}
	out := make([]Plan, 0, len(all))
	for _, p := range all {
		if dir, ok := px.ProjectDirOf(p); ok {
			p.ProjectDir = dir
		}
		out = append(out, p)
	}
	return out, nil
}
