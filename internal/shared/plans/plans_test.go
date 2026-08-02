package plans

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseFrontmatter(t *testing.T) {
	cases := []struct {
		name      string
		content   string
		wantTitle string
		wantSess  []string
	}{
		{
			name:      "title and sessions block",
			content:   "---\ntitle: My Plan\nsessions:\n  - fatal-mere-carat\n  - trim-soapy-ogle\n---\nbody",
			wantTitle: "My Plan",
			wantSess:  []string{"fatal-mere-carat", "trim-soapy-ogle"},
		},
		{
			name:      "quoted title, single session",
			content:   "---\ntitle: \"Quoted: Title\"\nsessions:\n  - only-one\n---\n",
			wantTitle: "Quoted: Title",
			wantSess:  []string{"only-one"},
		},
		{
			name:      "title only, no sessions",
			content:   "---\ntitle: Solo\nstatus: draft\n---\n",
			wantTitle: "Solo",
			wantSess:  nil,
		},
		{
			name:      "no frontmatter",
			content:   "# Just a heading\n\nsome text",
			wantTitle: "",
			wantSess:  nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			title, sessions := ParseFrontmatter(tc.content)
			if title != tc.wantTitle {
				t.Errorf("title = %q, want %q", title, tc.wantTitle)
			}
			if len(sessions) != len(tc.wantSess) {
				t.Fatalf("sessions = %v, want %v", sessions, tc.wantSess)
			}
			for i := range sessions {
				if sessions[i] != tc.wantSess[i] {
					t.Errorf("sessions[%d] = %q, want %q", i, sessions[i], tc.wantSess[i])
				}
			}
		})
	}
}

func TestList(t *testing.T) {
	root := t.TempDir()
	// Two sessions, one with a titled plan, one with a bare plan; plus noise.
	mustWrite(t, filepath.Join(root, "alpha-harp", "design.plan.md"),
		"---\ntitle: Alpha Design\nsessions:\n  - alpha-harp\n---\nbody")
	mustWrite(t, filepath.Join(root, "beta-harp", "rollout.plan.md"), "no frontmatter here")
	mustWrite(t, filepath.Join(root, "beta-harp", "notes.md"), "not a plan file")
	mustWrite(t, filepath.Join(root, "loose.txt"), "ignored")

	got, err := List(root)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("got %d plans, want 2: %+v", len(got), got)
	}
	// Sorted by session then name: alpha-harp/design, beta-harp/rollout.
	if got[0].Session != "alpha-harp" || got[0].Name != "design" || got[0].Title != "Alpha Design" {
		t.Errorf("plan[0] = %+v", got[0])
	}
	if len(got[0].Sessions) != 1 || got[0].Sessions[0] != "alpha-harp" {
		t.Errorf("plan[0].Sessions = %v", got[0].Sessions)
	}
	// Bare plan falls back to its name for the title.
	if got[1].Session != "beta-harp" || got[1].Name != "rollout" || got[1].Title != "rollout" {
		t.Errorf("plan[1] = %+v", got[1])
	}
}

func TestListMissingRootIsEmpty(t *testing.T) {
	got, err := List(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("List on missing root: %v", err)
	}
	if len(got) != 0 {
		t.Errorf("want empty, got %+v", got)
	}
}

// An unreadable session directory must be a LOUD failure, not a
// silently shorter list. Dropping a harp's whole plan set and returning
// success is indistinguishable from that session having no plans.
func TestListUnreadableSessionDirFailsLoudly(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "readable-harp", "ok.plan.md"), "---\ntitle: Fine\n---\n")
	blocked := filepath.Join(root, "blocked-harp")
	mustWrite(t, filepath.Join(blocked, "secret.plan.md"), "---\ntitle: Hidden\n---\n")
	if err := os.Chmod(blocked, 0o000); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	t.Cleanup(func() { _ = os.Chmod(blocked, 0o755) })
	if _, err := os.ReadDir(blocked); err == nil {
		t.Skip("session dir is still readable (running as root?) — cannot exercise the failure")
	}

	got, err := List(root)
	if err == nil {
		t.Fatalf("List succeeded with %d plans despite an unreadable session dir; "+
			"the blocked harp's plans vanished silently: %+v", len(got), got)
	}
	if !strings.Contains(err.Error(), "blocked-harp") {
		t.Errorf("error must name the session that could not be read, got: %v", err)
	}
}

// A session directory that disappears mid-scan is NOT a failure: it genuinely
// has no plans any more. Only a real read error is loud.
func TestListVanishedSessionDirIsNotAnError(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "alpha-harp", "design.plan.md"), "---\ntitle: A\n---\n")
	gone := filepath.Join(root, "gone-harp")
	if err := os.MkdirAll(gone, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(gone); err != nil {
		t.Fatal(err)
	}

	got, err := List(root)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Errorf("got %d plans, want 1: %+v", len(got), got)
	}
}

// TestList_FindsNestedPlanFiles pins the fix: List enumerated only
// <root>/<harp>/*.plan.md — exactly one level deep — while the paired
// watcher (internal/shared/watch, wired in internal/cli/plan_watch.go) is
// explicitly recursive. A plan nested one level deeper than that (this
// project's own arch-review session directories are shaped exactly this
// way) fired a change event with nothing in the re-queried list to show for
// it.
func TestList_FindsNestedPlanFiles(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "alpha-harp", "top.plan.md"), "---\ntitle: Top\n---\n")
	mustWrite(t, filepath.Join(root, "alpha-harp", "review", "nested.plan.md"), "---\ntitle: Nested\n---\n")
	mustWrite(t, filepath.Join(root, "alpha-harp", "review", "deep", "deeper.plan.md"), "no frontmatter")

	got, err := List(root)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d plans, want 3 (one top-level + two nested): %+v", len(got), got)
	}

	byName := map[string]Plan{}
	for _, p := range got {
		byName[p.Name] = p
	}
	nested, ok := byName["review/nested"]
	if !ok {
		t.Fatalf("nested plan not found by its relative name; got names: %v", namesOf(got))
	}
	if nested.Session != "alpha-harp" {
		t.Errorf("nested plan Session = %q, want alpha-harp", nested.Session)
	}
	if nested.Title != "Nested" {
		t.Errorf("nested plan Title = %q, want Nested", nested.Title)
	}
	deeper, ok := byName["review/deep/deeper"]
	if !ok {
		t.Fatalf("two-level-nested plan not found; got names: %v", namesOf(got))
	}
	if deeper.Session != "alpha-harp" {
		t.Errorf("two-level-nested plan Session = %q, want alpha-harp", deeper.Session)
	}
}

// TestPlan_JSONShape_IncludesSessions pins that Plan.Sessions has no
// in-repo reader (rg -n '\.Sessions\b' --type go -g '!*_test.go' finds only
// unrelated idx.Sessions hits in internal/sessions and
// internal/shared/plans/scoped.go). Its only possible consumer is the
// out-of-repo VS Code Plan view, whose wire contract this package cannot
// observe breaking. Rather than delete the field on unverifiable evidence,
// this golden test asserts the exact JSON shape List produces — including
// "sessions" — end to end through List/ParseFrontmatter, so a future change
// that silently drops or renames the field (removing ParseFrontmatter's
// sessions: block parsing, or changing the json tag) fails a test instead of
// only ever being noticed by a frontend nobody here can run.
func TestPlan_JSONShape_IncludesSessions(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "swift-amber-falcon", "v1-removal.plan.md"),
		"---\ntitle: V1 removal\nsessions:\n  - swift-amber-falcon\n  - busy-lived-denim\n---\nbody\n")

	got, err := List(root)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d plans, want 1: %+v", len(got), got)
	}
	plan := got[0]

	data, err := json.Marshal(plan)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}

	var wire map[string]any
	if err := json.Unmarshal(data, &wire); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	for _, key := range []string{"path", "name", "title", "session", "sessions"} {
		if _, ok := wire[key]; !ok {
			t.Errorf("wire JSON is missing %q — the VS Code Plan view's contract requires it; got keys: %v", key, wire)
		}
	}

	sessionsWire, ok := wire["sessions"].([]any)
	if !ok {
		t.Fatalf(`wire["sessions"] = %#v (%T), want a JSON array`, wire["sessions"], wire["sessions"])
	}
	if len(sessionsWire) != 2 {
		t.Fatalf(`wire["sessions"] = %v, want 2 entries (parsed from the frontmatter block)`, sessionsWire)
	}
	if sessionsWire[0] != "swift-amber-falcon" || sessionsWire[1] != "busy-lived-denim" {
		t.Errorf(`wire["sessions"] = %v, want [swift-amber-falcon busy-lived-denim] in frontmatter order`, sessionsWire)
	}
}

func namesOf(plans []Plan) []string {
	out := make([]string, len(plans))
	for i, p := range plans {
		out[i] = p.Name
	}
	return out
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
