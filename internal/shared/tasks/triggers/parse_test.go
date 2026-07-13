package triggers

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseVerdicts_TableDriven(t *testing.T) {
	cases := []struct {
		name    string
		raw     string
		wantErr bool
		check   func(t *testing.T, got []Verdict)
	}{
		{
			name: "plain JSON array",
			raw:  `[{"harp_id":"swift-amber-falcon","outcome":"fired","confidence":0.9,"evidence":["commit abc123"],"reasoning":"the CLI shipped"}]`,
			check: func(t *testing.T, got []Verdict) {
				require.Len(t, got, 1)
				assert.Equal(t, "swift-amber-falcon", got[0].HarpID)
				assert.Equal(t, Fired, got[0].Outcome)
				assert.Equal(t, 0.9, got[0].Confidence)
				assert.Equal(t, []string{"commit abc123"}, got[0].Evidence)
				assert.Equal(t, "the CLI shipped", got[0].Reasoning)
			},
		},
		{
			name: "fenced with json language tag",
			raw:  "```json\n[{\"harp_id\":\"a\",\"outcome\":\"not-fired\",\"confidence\":0.2,\"evidence\":[],\"reasoning\":\"nothing yet\"}]\n```",
			check: func(t *testing.T, got []Verdict) {
				require.Len(t, got, 1)
				assert.Equal(t, NotFired, got[0].Outcome)
			},
		},
		{
			name: "fenced with plain backticks",
			raw:  "```\n[{\"harp_id\":\"a\",\"outcome\":\"cannot-determine\",\"confidence\":0,\"evidence\":[],\"reasoning\":\"depends on a person\"}]\n```",
			check: func(t *testing.T, got []Verdict) {
				require.Len(t, got, 1)
				assert.Equal(t, CannotDetermine, got[0].Outcome)
			},
		},
		{
			name: "leading prose before the array",
			raw:  "Here is my triage:\n\n[{\"harp_id\":\"a\",\"outcome\":\"needs-investigation\",\"confidence\":0.5,\"evidence\":[],\"reasoning\":\"unclear\"}]",
			check: func(t *testing.T, got []Verdict) {
				require.Len(t, got, 1)
				assert.Equal(t, NeedsInvestigation, got[0].Outcome)
			},
		},
		{
			name: "confidence out of range is clamped, not rejected",
			raw:  `[{"harp_id":"a","outcome":"fired","confidence":1.7,"evidence":[],"reasoning":"x"},{"harp_id":"b","outcome":"fired","confidence":-3,"evidence":[],"reasoning":"y"}]`,
			check: func(t *testing.T, got []Verdict) {
				require.Len(t, got, 2)
				assert.Equal(t, 1.0, got[0].Confidence)
				assert.Equal(t, 0.0, got[1].Confidence)
			},
		},
		{
			name: "evidence strings containing brackets don't confuse the scan",
			raw:  `[{"harp_id":"a","outcome":"fired","confidence":0.8,"evidence":["see PR #12 [merged]"],"reasoning":"x"}]`,
			check: func(t *testing.T, got []Verdict) {
				require.Len(t, got, 1)
				assert.Equal(t, []string{"see PR #12 [merged]"}, got[0].Evidence)
			},
		},
		{
			name:    "empty array is valid (zero deferred tasks)",
			raw:     `[]`,
			wantErr: false,
			check: func(t *testing.T, got []Verdict) {
				assert.Empty(t, got)
			},
		},
		{
			name:    "no brackets at all",
			raw:     "I couldn't find any deferred tasks to evaluate.",
			wantErr: true,
		},
		{
			name:    "truncated JSON",
			raw:     `[{"harp_id":"a","outcom`,
			wantErr: true,
		},
		{
			name:    "malformed JSON inside brackets",
			raw:     `[{"harp_id": "a", "outcome": fired}]`,
			wantErr: true,
		},
		{
			name:    "missing harp_id",
			raw:     `[{"outcome":"fired","confidence":0.5,"evidence":[],"reasoning":"x"}]`,
			wantErr: true,
		},
		{
			name:    "blank harp_id",
			raw:     `[{"harp_id":"   ","outcome":"fired","confidence":0.5,"evidence":[],"reasoning":"x"}]`,
			wantErr: true,
		},
		{
			name:    "unrecognized outcome value",
			raw:     `[{"harp_id":"a","outcome":"probably","confidence":0.5,"evidence":[],"reasoning":"x"}]`,
			wantErr: true,
		},
		{
			name:    "outcome wrong case",
			raw:     `[{"harp_id":"a","outcome":"Fired","confidence":0.5,"evidence":[],"reasoning":"x"}]`,
			wantErr: true,
		},
		{
			name:    "empty string",
			raw:     "",
			wantErr: true,
		},
		{
			name:    "only whitespace",
			raw:     "   \n\t  ",
			wantErr: true,
		},
		{
			name:    "array of scalars, not objects",
			raw:     `[1, 2, 3]`,
			wantErr: true,
		},
		{
			name: "needs-investigation with a query request",
			raw:  `[{"harp_id":"a","outcome":"needs-investigation","confidence":0.4,"evidence":[],"reasoning":"unclear","queries":[{"type":"path_exists","path":"internal/foo"},{"type":"grep","pattern":"func Foo"}]}]`,
			check: func(t *testing.T, got []Verdict) {
				require.Len(t, got, 1)
				require.Len(t, got[0].Queries, 2)
				assert.Equal(t, QueryPathExists, got[0].Queries[0].Type)
				assert.Equal(t, "internal/foo", got[0].Queries[0].Path)
				assert.Equal(t, QueryGrep, got[0].Queries[1].Type)
				assert.Equal(t, "func Foo", got[0].Queries[1].Pattern)
			},
		},
		{
			name: "a model-supplied cached field is always reset to false",
			raw:  `[{"harp_id":"a","outcome":"fired","confidence":0.9,"evidence":[],"reasoning":"x","cached":true}]`,
			check: func(t *testing.T, got []Verdict) {
				require.Len(t, got, 1)
				assert.False(t, got[0].Cached, "Cached must never be model-controlled")
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Must never panic on adversarial input, regardless of outcome.
			assert.NotPanics(t, func() {
				got, err := ParseVerdicts(tc.raw)
				if tc.wantErr {
					assert.Error(t, err)
					return
				}
				require.NoError(t, err)
				if tc.check != nil {
					tc.check(t, got)
				}
			})
		})
	}
}

// stripCodeFence and extractJSONArray are asserted DIRECTLY, not only through
// ParseVerdicts, because the bracket scan is deliberately tolerant and therefore
// masks damage in the fence stripper: an index slip that mangles the stripped
// text usually still leaves a findable "[...]" behind it, so ParseVerdicts still
// succeeds and the bug goes unseen. Pinning each helper's exact output is what
// actually holds their index arithmetic in place. Every case below is chosen so
// that an off-by-one produces a different string, not merely a different-looking
// one that happens to re-parse.
func TestStripCodeFence(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"no fence is returned untouched", `[{"a":1}]`, `[{"a":1}]`},
		{"json language tag", "```json\n[1]\n```", "[1]"},
		{"bare fence", "```\n[1]\n```", "[1]"},
		// A one-character opener token: the whole opener LINE is dropped
		// whatever its length, so the drop cannot be tuned to a fixed offset.
		{"single-character language tag", "```r\n[1]\n```", "[1]"},
		{"inline fence, no newlines", "```[1]```", "[1]"},
		{"single character inline", "```x```", "x"},
		{"unterminated fence keeps its content", "```json\n[1]", "[1]"},
		{"opener only", "```", ""},
		{"trailing prose after the closer is dropped", "```json\n[1]\n```\nhope that helps", "[1]"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, stripCodeFence(tc.in))
		})
	}
}

func TestExtractJSONArray(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"bare array", "[1]", "[1]"},
		// Both brackets sit at the extreme low indices here, which is where an
		// inverted sentinel (-1 read as 1) stops being distinguishable from a
		// real position.
		{"empty array", "[]", "[]"},
		{"one character of prose before the array", "x[1]", "[1]"},
		{"prose on both sides", "here you go: [1] hope that helps", "[1]"},
		{"nested brackets span to the LAST closer", `[{"e":["a [b] c"]}]`, `[{"e":["a [b] c"]}]`},
		{"no brackets", "no array here", ""},
		{"opener with no closer", "[1", ""},
		{"closer with no opener", "1]", ""},
		{"closer before opener", "] [", ""},
		{"empty input", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, extractJSONArray(tc.in))
		})
	}
}

// ParseVerdicts consumes untrusted model output, so its contract is a property,
// not a set of examples: whatever it is handed, it must not panic, and it must
// never hand back a partial or half-validated verdict set — a caller that
// accepted a truncated "cannot-determine" as "fired" would revive a task on
// evidence that was never established.
//
// The seed corpus is the shapes real models actually emit; the fuzzer's job is
// the space between them.
func FuzzParseVerdicts(f *testing.F) {
	seeds := []string{
		`[{"harp_id":"a","outcome":"fired","confidence":0.9,"evidence":["abc123"],"reasoning":"shipped"}]`,
		"```json\n[{\"harp_id\":\"a\",\"outcome\":\"not-fired\",\"confidence\":0.2,\"evidence\":[],\"reasoning\":\"no\"}]\n```",
		"Here is my triage:\n\n[{\"harp_id\":\"a\",\"outcome\":\"needs-investigation\",\"confidence\":0.5,\"evidence\":[],\"reasoning\":\"unclear\"}]",
		`[{"harp_id":"a","outcome":"needs-investigation","confidence":0.4,"reasoning":"x","queries":[{"type":"grep","pattern":"func Foo"}]}]`,
		`[{"harp_id":"a","outcome":"fired","confidence":1.7,"evidence":[],"reasoning":"x"}]`,
		`[]`,
		`[{"harp_id":"a","outcom`, // truncated mid-key
		"```",
		"] [",
		"",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, raw string) {
		got, err := ParseVerdicts(raw)
		if err != nil {
			assert.Nil(t, got, "a failed parse must yield NO verdicts, never a partial set")
			return
		}
		for i, v := range got {
			assert.NotEmpty(t, strings.TrimSpace(v.HarpID), "verdict %d was accepted with a blank harp_id", i)
			assert.True(t, v.Outcome.Valid(), "verdict %d was accepted with outcome %q", i, v.Outcome)
			assert.GreaterOrEqual(t, v.Confidence, 0.0, "verdict %d escaped the confidence clamp", i)
			assert.LessOrEqual(t, v.Confidence, 1.0, "verdict %d escaped the confidence clamp", i)
			assert.False(t, v.Cached, "verdict %d: Cached is caller-stamped and must never come from the model", i)
		}
	})
}
