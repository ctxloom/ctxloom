package cli

import (
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/operations"
)

// captureStdout runs fn with os.Stdout redirected to a pipe and returns what
// fn wrote to it. Mirrors captureStderr (llm_resolve_test.go).
func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	orig := os.Stdout
	os.Stdout = w
	defer func() { os.Stdout = orig }()

	fn()

	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	out, err := io.ReadAll(r)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

func TestSearchScopes(t *testing.T) {
	tests := []struct {
		name                  string
		localOnly, remoteOnly bool
		wantLocal, wantRemote bool
	}{
		{"neither flag searches both", false, false, true, true},
		{"local only", true, false, true, false},
		{"remote only", false, true, false, true},
		{"both flags searches both", true, true, true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotLocal, gotRemote := searchScopes(tt.localOnly, tt.remoteOnly)
			if gotLocal != tt.wantLocal || gotRemote != tt.wantRemote {
				t.Errorf("searchScopes(%v, %v) = (%v, %v), want (%v, %v)",
					tt.localOnly, tt.remoteOnly, gotLocal, gotRemote, tt.wantLocal, tt.wantRemote)
			}
		})
	}
}

func TestResolveSearchTypes(t *testing.T) {
	tests := []struct {
		name       string
		itemType   string
		wantLocal  []string
		wantRemote []string
		wantErr    bool
	}{
		{"empty searches all", "", []string{"fragment", "command", "skill", "profile", "mcp_server"}, []string{"bundle"}, false},
		{"fragment is local-only", "fragment", []string{"fragment"}, nil, false},
		{"command is local-only", "command", []string{"command"}, nil, false},
		{"skill is local-only", "skill", []string{"skill"}, nil, false},
		{"mcp_server is local-only", "mcp_server", []string{"mcp_server"}, nil, false},
		{"profile is local-only", "profile", []string{"profile"}, nil, false},
		{"bundle is remote-only", "bundle", nil, []string{"bundle"}, false},
		{"unknown type errors", "widget", nil, nil, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotLocal, gotRemote, err := resolveSearchTypes(tt.itemType)
			if (err != nil) != tt.wantErr {
				t.Fatalf("resolveSearchTypes(%q) err = %v, wantErr %v", tt.itemType, err, tt.wantErr)
			}
			if tt.wantErr {
				return
			}
			if !reflect.DeepEqual(gotLocal, tt.wantLocal) {
				t.Errorf("local = %v, want %v", gotLocal, tt.wantLocal)
			}
			if !reflect.DeepEqual(gotRemote, tt.wantRemote) {
				t.Errorf("remote = %v, want %v", gotRemote, tt.wantRemote)
			}
		})
	}
}

// TestResolveSearchTypes_UnknownTypeErrorNamesValidValues pins a guided-error
// requirement (docs/cli-ux-principles.md §6): the message must name the
// concrete valid values and point at --help, not just say "unknown type".
func TestResolveSearchTypes_UnknownTypeErrorNamesValidValues(t *testing.T) {
	_, _, err := resolveSearchTypes("widget")
	if err == nil {
		t.Fatal("expected an error for an unknown type")
	}
	msg := err.Error()
	for _, want := range []string{"widget", "fragment", "command", "skill", "profile", "bundle", "mcp_server", "--help"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q missing %q", msg, want)
		}
	}
	// "prompt" is stale terminology from before the prompt->command rename;
	// the error must not resurrect it.
	if strings.Contains(msg, "prompt") {
		t.Errorf("error %q still mentions the retired \"prompt\" type name", msg)
	}
}

// TestNoteHiddenLocalMatches pins the anti-silent-truncation hint for
// `ctxloom search`: when the local searchLocalLimit cap hid matches, a
// one-line stderr note says how many and how to narrow; zero hidden stays
// quiet (mirrors taskloom's noteHiddenMatches).
func TestNoteHiddenLocalMatches(t *testing.T) {
	cases := []struct {
		name      string
		hidden    int
		wantParts []string
	}{
		{
			name:      "matches hidden by the cap",
			hidden:    12,
			wantParts: []string{"12 more local match(es)", "100-result cap", "--type", "--tag"},
		},
		{
			name:   "nothing hidden stays quiet",
			hidden: 0,
		},
		{
			name:   "negative (defensive) stays quiet",
			hidden: -1,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var buf strings.Builder
			noteHiddenLocalMatches(&buf, c.hidden)
			out := buf.String()
			if len(c.wantParts) == 0 {
				if out != "" {
					t.Fatalf("expected no hint, got %q", out)
				}
				return
			}
			for _, part := range c.wantParts {
				if !strings.Contains(out, part) {
					t.Fatalf("hint %q missing %q", out, part)
				}
			}
			if !strings.HasSuffix(out, "\n") || strings.Count(out, "\n") != 1 {
				t.Fatalf("hint must be exactly one line, got %q", out)
			}
		})
	}
}

// TestPrintLocalResultsIncludesSkills guards against a silent-drop bug found
// during this pass: operations.SearchContent's default type set already
// included "skill" results, but the CLI's rendering table had no "skill" row
// — a skill match would be computed, counted in the total, and then vanish
// from the printed output with no error and no trace. Pins that a skill
// result is actually rendered, not just counted.
func TestPrintLocalResultsIncludesSkills(t *testing.T) {
	out := captureStdout(t, func() {
		printLocalResults([]operations.SearchResult{
			{Type: "skill", Name: "code-review", Tags: []string{"review"}},
		})
	})
	if !strings.Contains(out, "Skills:") {
		t.Fatalf("expected a Skills: section, got %q", out)
	}
	if !strings.Contains(out, "code-review") {
		t.Fatalf("expected the skill name to render, got %q", out)
	}
}
