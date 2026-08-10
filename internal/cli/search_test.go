package cli

import (
	"bytes"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/config"
	"github.com/ctxloom/ctxloom/internal/operations"
	"github.com/ctxloom/ctxloom/internal/projectroot"
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

// A search whose local AND remote halves both failed used to exit 0
// reporting "No results found." — indistinguishable from a genuinely empty
// library. searchFullyFailed is the decision runUnifiedSearch defers to:
// "every scope that was actually attempted came back with an error" — never
// true for a scope that was never queried at all (localOnly/remoteOnly
// narrowing, or a --type filter with no local/remote types), since that is
// legitimately "nothing to report on this half", not a failure.
func TestSearchFullyFailed(t *testing.T) {
	boom := os.ErrClosed // any non-nil error stand-in

	tests := []struct {
		name                            string
		localAttempted, remoteAttempted bool
		localErr, remoteErr             error
		want                            bool
	}{
		{"both attempted, both failed", true, true, boom, boom, true},
		{"both attempted, only local failed", true, true, boom, nil, false},
		{"both attempted, only remote failed", true, true, nil, boom, false},
		{"both attempted, neither failed", true, true, nil, nil, false},
		{"only local attempted, and it failed", true, false, boom, nil, true},
		{"only remote attempted, and it failed", false, true, nil, boom, true},
		{"neither attempted (nothing to fail)", false, false, nil, nil, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := searchFullyFailed(tt.localAttempted, tt.localErr, tt.remoteAttempted, tt.remoteErr)
			if got != tt.want {
				t.Errorf("searchFullyFailed(%v, %v, %v, %v) = %v, want %v",
					tt.localAttempted, tt.localErr, tt.remoteAttempted, tt.remoteErr, got, tt.want)
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
	var buf bytes.Buffer
	printLocalResults(&buf, []operations.SearchResult{
		{Type: "skill", Name: "code-review", Tags: []string{"review"}},
	})
	out := buf.String()
	if !strings.Contains(out, "Skills:") {
		t.Fatalf("expected a Skills: section, got %q", out)
	}
	if !strings.Contains(out, "code-review") {
		t.Fatalf("expected the skill name to render, got %q", out)
	}
}

// TestSearchWritesToTheCommandWriter pins where `ctxloom search` sends its
// human output. Every other command in this package renders through
// cmd.OutOrStdout(); search's five text renderers wrote to process-global
// os.Stdout via fmt.Printf/fmt.Println, so a caller that redirects the command
// (cobra's SetOut — what the format-coverage harness, the acceptance suite and
// any embedding frontend use) captured an EMPTY buffer while the text went
// somewhere it never asked for. The empty-result line is the smallest complete
// witness: it is the whole of search's output for that run.
func TestSearchWritesToTheCommandWriter(t *testing.T) {
	dir := t.TempDir()
	t.Setenv(projectroot.EnvVar, dir)
	config.Invalidate()

	var out, errOut bytes.Buffer
	rootCmd.SetOut(&out)
	rootCmd.SetErr(&errOut)
	rootCmd.SetArgs([]string{"search", "--local", "zzz-no-such-content-anywhere", "--format", "text"})
	t.Cleanup(func() {
		rootCmd.SetOut(nil)
		rootCmd.SetErr(nil)
		rootCmd.SetArgs(nil)
	})

	require.NoError(t, rootCmd.Execute(), "stderr: %s", errOut.String())
	assert.Contains(t, out.String(), "No results found.",
		"search's text output must reach the command's own writer, not process-global stdout")
}

// TestPrintUnifiedResults_RendersToItsWriter pins the same contract for the
// populated path: header, local grouping and the remote table all land on the
// writer they were handed.
func TestPrintUnifiedResults_RendersToItsWriter(t *testing.T) {
	var buf bytes.Buffer
	printUnifiedResults(&buf,
		[]operations.SearchResult{{Type: "fragment", Name: "go-testing", Tags: []string{"golang"}, Source: "local"}},
		[]operations.SearchRemoteEntry{{Type: "bundle", Remote: "acme", Name: "acme-tools", Tags: []string{"tools"}}},
	)
	out := buf.String()
	assert.Contains(t, out, "Results (2):")
	assert.Contains(t, out, "Fragments:")
	assert.Contains(t, out, "go-testing")
	assert.Contains(t, out, "Remote:")
	assert.Contains(t, out, "acme-tools")
}

// TestPrintRemoteResults_TableGeometry is the characterization pin for the
// remote results table. Its five widths (three format widths, two truncation
// caps) governed one table but were written out five separate times, with the
// rule under the header HAND-DRAWN to match — so a width change silently left
// the rule at the old size, and the name column's 20-wide cell truncating at
// 18 read as an off-by-two rather than the deliberate gutter it is. Behaviour
// is unchanged by that cleanup (wave-brief §4 class 2: pure complexity
// reduction, no test can discriminate), so this pins the rendered bytes AND
// the structural invariant the hand-drawn rule could violate: every ┼ sits
// exactly under the │ above it.
func TestPrintRemoteResults_TableGeometry(t *testing.T) {
	var buf bytes.Buffer
	printRemoteResults(&buf, []operations.SearchRemoteEntry{
		{Type: "bundle", Remote: "acme", Name: "short", Tags: []string{"a"}},
		{Type: "bundle", Remote: "a-very-long-remote-name", Name: "a-name-that-is-way-too-long-to-fit",
			Tags: []string{"tag-one", "tag-two", "tag-three", "tag-four"}},
	})

	const want = "Remote:\n" +
		"  Type     │ Remote       │ Name                 │ Tags\n" +
		"  ─────────┼──────────────┼──────────────────────┼────────────\n" +
		"  bundle   │ acme         │ short                │ a\n" +
		"  bundle   │ a-very-long-remote-name │ a-name-that-is-...   │ tag-one, tag-two,...\n" +
		"\n" +
		"Use one: add its ref to a profile (ctxloom profile create/modify), then ctxloom deps pull\n"
	assert.Equal(t, want, buf.String())

	lines := strings.Split(buf.String(), "\n")
	assert.Equal(t, runeOffsets(lines[1], '│'), runeOffsets(lines[2], '┼'),
		"the rule's junctions must sit under the header's separators — the whole reason the widths cannot be written out twice")
	assert.Equal(t, runeOffsets(lines[1], '│'), runeOffsets(lines[3], '│'),
		"a row that fits every column must align with the header")
}

// Four of the six Ellipsize call sites this table is cited alongside cap a
// TRAILING field, not a cell: the Tags column runs to end of line (see
// remoteTagsCap doc), and remote_discover's description, bundle_list's fragment
// preview and bundle_distill's firstLineTruncated are all last on their line.
// A byte cap on a trailing field cannot misalign anything, whatever the script
// — which is what this pins, with a column of CJK and emoji tags that measures
// 2 display cells per rune and 3-4 bytes per rune, so byte, rune and width
// counts all disagree.
func TestPrintRemoteResults_TheTrailingColumnsCapCannotMisalignTheTable(t *testing.T) {
	var ascii, wide bytes.Buffer
	printRemoteResults(&ascii, []operations.SearchRemoteEntry{
		{Type: "bundle", Remote: "acme", Name: "short", Tags: []string{"a"}},
	})
	printRemoteResults(&wide, []operations.SearchRemoteEntry{
		{Type: "bundle", Remote: "acme", Name: "short", Tags: []string{"日本語のタグ", "絵文字🙂のタグ"}},
	})

	asciiRow := strings.Split(ascii.String(), "\n")[3]
	wideRow := strings.Split(wide.String(), "\n")[3]
	assert.Equal(t, runeOffsets(asciiRow, '│'), runeOffsets(wideRow, '│'),
		"the last column is unpadded, so its byte cap moves no separator")
}

// runeOffsets reports the rune (not byte) index of every occurrence of r,
// which is what column alignment is actually measured in once box-drawing
// characters are in play.
func runeOffsets(s string, r rune) []int {
	var out []int
	for i, c := range []rune(s) {
		if c == r {
			out = append(out, i)
		}
	}
	return out
}
