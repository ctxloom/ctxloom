package plans

import (
	"path/filepath"
	"testing"

	"github.com/ctxloom/ctxloom/internal/testsupport"
)

// writeSessionIndex writes a minimal ~/.ctxloom/sessions/index.yaml binding
// each harp to a project dir — the join `plan list` scoping rides on.
func writeSessionIndex(t *testing.T, home string, harpToDir map[string]string) {
	t.Helper()
	body := "sessions:\n"
	for harp, dir := range harpToDir {
		body += "    - harp_name: " + harp + "\n      project_dir: " + dir + "\n"
	}
	mustWrite(t, filepath.Join(home, ".ctxloom", "sessions", "index.yaml"), body)
}

// seedPlans lays down one plan per harp under the isolated home's sessions dir.
func seedPlans(t *testing.T, home string, harps ...string) {
	t.Helper()
	for _, h := range harps {
		mustWrite(t, filepath.Join(home, ".ctxloom", "sessions", h, "design.plan.md"),
			"---\ntitle: "+h+" plan\n---\nbody")
	}
}

// A scoped listing must return ONLY the plans whose session ran in the given
// project dir — not every session on the machine.
func TestListHomeScoped_ReturnsOnlyThisProject(t *testing.T) {
	home := testsupport.Isolate(t)
	seedPlans(t, home, "mine-one", "mine-two", "theirs")
	writeSessionIndex(t, home, map[string]string{
		"mine-one": "/work/alpha",
		"mine-two": "/work/alpha",
		"theirs":   "/work/beta",
	})

	matched, unattributed, err := ListHomeScoped("/work/alpha")
	if err != nil {
		t.Fatalf("ListHomeScoped: %v", err)
	}
	if len(unattributed) != 0 {
		t.Errorf("unattributed = %+v, want none", unattributed)
	}
	if len(matched) != 2 {
		t.Fatalf("matched = %d plans, want 2: %+v", len(matched), matched)
	}
	for _, p := range matched {
		if p.Session == "theirs" {
			t.Errorf("another project's plan leaked into the scoped listing: %+v", p)
		}
		if p.ProjectDir != "/work/alpha" {
			t.Errorf("plan %q ProjectDir = %q, want /work/alpha", p.Session, p.ProjectDir)
		}
	}
}

// A plan whose session has no index entry must NEVER silently disappear: it
// comes back in the unattributed bucket so the caller can show it marked.
func TestListHomeScoped_UnattributedPlansSurvive(t *testing.T) {
	home := testsupport.Isolate(t)
	seedPlans(t, home, "mine", "orphan")
	writeSessionIndex(t, home, map[string]string{"mine": "/work/alpha"})

	matched, unattributed, err := ListHomeScoped("/work/alpha")
	if err != nil {
		t.Fatalf("ListHomeScoped: %v", err)
	}
	if len(matched) != 1 || matched[0].Session != "mine" {
		t.Errorf("matched = %+v, want just mine", matched)
	}
	if len(unattributed) != 1 || unattributed[0].Session != "orphan" {
		t.Fatalf("unattributed = %+v, want just orphan", unattributed)
	}
	if unattributed[0].ProjectDir != "" {
		t.Errorf("unattributed plan carries a ProjectDir %q", unattributed[0].ProjectDir)
	}
}

// ProjectDirOf is the single-plan form of the same join.
func TestProjectDirOf(t *testing.T) {
	home := testsupport.Isolate(t)
	writeSessionIndex(t, home, map[string]string{"known": "/work/alpha"})

	px, err := LoadProjectIndex()
	if err != nil {
		t.Fatalf("LoadProjectIndex: %v", err)
	}
	dir, ok := px.ProjectDirOf(Plan{Session: "known"})
	if !ok || dir != "/work/alpha" {
		t.Errorf("ProjectDirOf(known) = %q, %v; want /work/alpha, true", dir, ok)
	}
	if _, ok := px.ProjectDirOf(Plan{Session: "stranger"}); ok {
		t.Error("ProjectDirOf(stranger) reported ok for a session with no index entry")
	}
}

// Path comparison must survive cosmetic differences (trailing separator).
func TestListHomeScoped_PathsAreCleaned(t *testing.T) {
	home := testsupport.Isolate(t)
	seedPlans(t, home, "mine")
	writeSessionIndex(t, home, map[string]string{"mine": "/work/alpha/"})

	matched, _, err := ListHomeScoped("/work/alpha")
	if err != nil {
		t.Fatalf("ListHomeScoped: %v", err)
	}
	if len(matched) != 1 {
		t.Fatalf("matched = %+v, want the plan despite the trailing slash", matched)
	}
}
