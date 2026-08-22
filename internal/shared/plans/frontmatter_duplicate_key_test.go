package plans

import (
	"bytes"
	"io"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/shared/clidiag"
)

// Frontmatter with a repeated key is TOLERATED, resolved LAST-WINS, and
// announced. Ruled 2026-08-22: a plan whose frontmatter repeats a key is still
// a plan, the most recent value is the one the author most recently meant, and
// the reader says out loud that it picked.
//
// The regression this pins: ParseFrontmatter decoded the block straight into a
// struct, and yaml.v3 rejects a repeated key when decoding into a struct AND
// discards the WHOLE mapping — so a document that repeated `title:` lost its
// `sessions:` list to the reader (`taskloom plan list` showed none) while the
// paired writer, memory.StampPlanFile, kept appending to it.
//
// Every case here asserts all three halves — the winning value, the survival of
// the untouched keys, and a warning that NAMES the duplicated key — because
// each one alone is satisfiable by a reader that gets the other two wrong.
func TestParseFrontmatter_DuplicateKey_LastWinsAndWarns(t *testing.T) {
	cases := []struct {
		name         string
		content      string
		wantTitle    string
		wantSessions []string
		wantWarnings []string
	}{
		{
			name:         "a repeated title takes the last value and does not cost the sessions list",
			content:      "---\ntitle: A\nsessions: [harp-one]\ntitle: B\n---\nbody\n",
			wantTitle:    "B",
			wantSessions: []string{"harp-one"},
			wantWarnings: []string{`duplicate frontmatter key "title"; using the last value`},
		},
		{
			name:         "a repeated sessions list takes the last list, not the first and not their union",
			content:      "---\ntitle: T\nsessions: [one]\nsessions: [two, three]\n---\n",
			wantTitle:    "T",
			wantSessions: []string{"two", "three"},
			wantWarnings: []string{`duplicate frontmatter key "sessions"; using the last value`},
		},
		{
			name:         "a key repeated three times takes the last and warns once, not twice",
			content:      "---\ntitle: A\ntitle: B\ntitle: C\n---\n",
			wantTitle:    "C",
			wantWarnings: []string{`duplicate frontmatter key "title"; using the last value`},
		},
		{
			name:         "two different duplicated keys are each named",
			content:      "---\ntitle: A\nsessions: [one]\ntitle: B\nsessions: [two]\n---\n",
			wantTitle:    "B",
			wantSessions: []string{"two"},
			wantWarnings: []string{
				`duplicate frontmatter key "title"; using the last value`,
				`duplicate frontmatter key "sessions"; using the last value`,
			},
		},
		{
			name:      "a duplicated key this package does not read is still resolved and still named",
			content:   "---\nstatus: draft\ntitle: T\nstatus: final\n---\n",
			wantTitle: "T",
			wantWarnings: []string{
				`duplicate frontmatter key "status"; using the last value`,
			},
		},
		{
			name:         "frontmatter with no duplicate warns about nothing",
			content:      "---\ntitle: T\nsessions: [one]\n---\n",
			wantTitle:    "T",
			wantSessions: []string{"one"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			restore := clidiag.SetSink(&buf)
			defer restore()

			title, sessions := ParseFrontmatter(tc.content)

			assert.Equal(t, tc.wantTitle, title, "title from:\n%s", tc.content)
			assert.Equal(t, tc.wantSessions, sessions, "sessions from:\n%s", tc.content)

			got := warningLines(buf.String())
			require.Len(t, got, len(tc.wantWarnings),
				"one warning per duplicated key, got:\n%s", buf.String())
			for i, want := range tc.wantWarnings {
				assert.Equal(t, want, got[i],
					"warning %d must name the duplicated key and say which value won", i)
			}
		})
	}
}

// warningLines strips clidiag's "<prog>: warning: " stamp from each emitted
// line and returns the messages in order. The stamp is clidiag's to own and is
// pinned by its own tests; what this file asserts is the message body, so a
// warning gutted down to the prefix cannot pass.
func warningLines(out string) []string {
	var msgs []string
	for _, line := range strings.Split(strings.TrimSuffix(out, "\n"), "\n") {
		if line == "" {
			continue
		}
		_, msg, found := strings.Cut(line, ": warning: ")
		if !found {
			msgs = append(msgs, "UNSTAMPED: "+line)
			continue
		}
		msgs = append(msgs, msg)
	}
	return msgs
}

// The duplicate-key warning goes to STDERR, never stdout. `taskloom plan list`
// prints a machine-readable listing on stdout that an out-of-repo VS Code plan
// view parses; one warning line on that stream corrupts it for every consumer.
// This test swaps the real os.Stdout/os.Stderr rather than using clidiag's sink,
// because the sink is exactly the thing under suspicion: it proves which FILE
// the default (un-redirected) warning reaches.
func TestParseFrontmatter_DuplicateKeyWarning_GoesToStderrNotStdout(t *testing.T) {
	outR, outW, err := os.Pipe()
	require.NoError(t, err)
	errR, errW, err := os.Pipe()
	require.NoError(t, err)

	origOut, origErr := os.Stdout, os.Stderr
	os.Stdout, os.Stderr = outW, errW
	title, _ := ParseFrontmatter("---\ntitle: A\ntitle: B\n---\n")
	os.Stdout, os.Stderr = origOut, origErr
	require.NoError(t, outW.Close())
	require.NoError(t, errW.Close())

	stdout, err := io.ReadAll(outR)
	require.NoError(t, err)
	stderr, err := io.ReadAll(errR)
	require.NoError(t, err)

	assert.Equal(t, "B", title, "the last value still wins when nothing redirects the sink")
	assert.Empty(t, string(stdout), "nothing may reach stdout: plan list's stdout is parsed")
	assert.Contains(t, string(stderr), `duplicate frontmatter key "title"; using the last value`,
		"the warning belongs on stderr")
}
