package plans

import (
	"os"
	"path/filepath"
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

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
