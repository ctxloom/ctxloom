package harpmarker

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFormat(t *testing.T) {
	if got := Format("plump-loose-sash"); got != `<ctxloom name="plump-loose-sash" kind="harp" />` {
		t.Fatalf("Format = %q", got)
	}
	if got := Format(""); got != "" {
		t.Fatalf("Format(\"\") = %q, want empty", got)
	}
}

func TestFind(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"clean marker", `prose <ctxloom name="swift-amber-falcon" kind="harp" /> more`, "swift-amber-falcon"},
		{"attr order kind first", `<ctxloom kind="harp" name="bold-crimson-thunder" />`, "bold-crimson-thunder"},
		{"no marker", `<ctxloom-context>body</ctxloom-context>`, ""},
		{"wrong kind", `<ctxloom name="x" kind="resumed-from" />`, ""},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Find(tc.in); got != tc.want {
				t.Fatalf("Find(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// jsonString marshals s as a JSON string value (with default HTML escaping, so
// '<' becomes <), matching what encoding/json emits in the real hook path.
func jsonString(t *testing.T, s string) string {
	t.Helper()
	b, err := json.Marshal(s)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestScan_DirectAdditionalContext(t *testing.T) {
	// additionalContext delivered as a single-nested hook output string.
	marker := Format("single-nest-harp")
	hookOut := `{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":` + jsonString(t, marker) + `}}`
	line := `{"type":"system","content":` + jsonString(t, hookOut) + `}`
	if got := Scan([]byte(line)); got != "single-nest-harp" {
		t.Fatalf("Scan single-nest = %q", got)
	}
}

func TestScan_AttachmentDoubleNested(t *testing.T) {
	// The real Claude shape: attachment.stdout holds the hook's JSON output,
	// itself holding additionalContext — two layers of JSON escaping over the
	// marker, whose '<'/'>' are already </> from the hook encoder.
	marker := Format("plump-loose-sash")
	hookOut := `{"hookSpecificOutput":{"hookEventName":"SessionStart","additionalContext":` + jsonString(t, marker) + `}}`
	line := `{"parentUuid":null,"isSidechain":false,"attachment":{"type":"hook_success","hookName":"SessionStart:clear","stdout":` + jsonString(t, hookOut) + `}}`

	// Sanity: the raw line must NOT contain the clean marker (it is escaped),
	// so Scan is genuinely exercising the decode path, not a raw substring hit.
	if bytes.Contains([]byte(line), []byte(marker)) {
		t.Fatal("fixture leaks an unescaped marker; test would not exercise decoding")
	}
	if got := Scan([]byte(line)); got != "plump-loose-sash" {
		t.Fatalf("Scan double-nest = %q, want plump-loose-sash", got)
	}
}

func TestScan_NoMarker(t *testing.T) {
	line := `{"type":"user","message":{"role":"user","content":"just some work, no marker"}}`
	if got := Scan([]byte(line)); got != "" {
		t.Fatalf("Scan no-marker = %q, want empty", got)
	}
}

func TestScan_MalformedJSON(t *testing.T) {
	if got := Scan([]byte(`not json at all`)); got != "" {
		t.Fatalf("Scan malformed = %q, want empty", got)
	}
}

// TestFormat_NeverEmitsAMarkerThatCannotIdentifyItsHarp pins the round-trip
// contract Format's whole purpose depends on: whatever it returns must name
// the harp it was given when read back. harp.Validate is deliberately
// PERMISSIVE on charset (a harp is renameable to whatever a human finds
// memorable; it rejects only path separators, control characters and edge
// whitespace), so '"' and '>' both arrive here as legal harp names.
//
// Both are worse than they look. A '"' closes the name attribute early, so
// the marker reads back as a DIFFERENT, possibly real harp — silent
// misattribution, not a parse failure. A '>' closes the element before
// kind="harp" is reached, so the marker is unrecoverable. Emitting either is
// worse than emitting nothing, because the only caller (cli.emitHarpMarker)
// reports an empty return loudly and cannot detect a corrupt one at all.
func TestFormat_NeverEmitsAMarkerThatCannotIdentifyItsHarp(t *testing.T) {
	names := []string{
		"plump-loose-sash",
		"Ünicode-harp",
		"harp with spaces",
		`bad"name`,                      // closes the attribute early
		`a>b`,                           // closes the element early
		`a<b`,                           // opens a second element
		`x" kind="harp" name="evil`,     // attribute injection
		`y" kind="harp" /><ctxloom n="`, // element injection
	}
	for _, name := range names {
		t.Run(name, func(t *testing.T) {
			got := Format(name)
			if got == "" {
				return // refused outright: the caller reports it
			}
			if back := Find(got); back != name {
				t.Fatalf("Format(%q) = %q, which reads back as %q — a marker must name its own harp or not exist", name, got, back)
			}
		})
	}
}

// TestScan_IsDeterministicAcrossObjectKeys pins U112-F06: findInValue walked
// map[string]any in Go's RANDOMIZED range order, so a line carrying two harp
// markers under different object keys resolved to an arbitrary one — the same
// bytes producing a different answer between runs of the same binary. Measured
// before the fix: 500 scans of the fixture below split 432/68 across the two
// harps.
//
// A resolver of last resort that answers differently each time is worse than
// one that answers wrongly, because nothing downstream can detect it. Which
// harp wins is arbitrary; that it is STABLE is not.
func TestScan_IsDeterministicAcrossObjectKeys(t *testing.T) {
	line := []byte(`{"k1":` + jsonString(t, Format("aaa-aaa-aaa")) +
		`,"k2":` + jsonString(t, Format("zzz-zzz-zzz")) + `}`)

	first := Scan(line)
	require.NotEmpty(t, first, "fixture must carry a recoverable marker")
	for i := range 500 {
		if got := Scan(line); got != first {
			t.Fatalf("scan %d of identical bytes returned %q, first returned %q", i, got, first)
		}
	}
}

// TestFind_KindAttributeIsTheOnlyDiscriminator pins U112-F08. The package doc
// used to say the marker's self-closing form was "deliberately distinct from
// the <ctxloom-context>…</ctxloom-context> content wrapper", which reads as
// the SHAPE doing the separating. It does not, and never did: markerRe's
// <ctxloom\b matches the content wrapper's prefix ('-' is a word boundary)
// and its trailing slash is optional.
//
// The corrected doc now asserts that kind="harp" is the only discriminator.
// These cases are what makes that claim checkable rather than another
// assertion nobody can verify — each arm fails if the element name or the
// self-closing shape silently becomes load-bearing.
func TestFind_KindAttributeIsTheOnlyDiscriminator(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		// The kind attribute alone admits or rejects.
		{"wrapper without kind is not a marker", `<ctxloom-context>body</ctxloom-context>`, ""},
		{"element named ctxloom without kind is not a marker", `<ctxloom name="nope" />`, ""},

		// Shape is NOT the discriminator: drop the self-closing slash and the
		// element still matches, because only kind="harp" is required.
		{"non-self-closing element still matches", `<ctxloom name="open-tag-harp" kind="harp">`, "open-tag-harp"},

		// Element name is NOT the discriminator either: the content wrapper's
		// own spelling matches once it carries the attribute. Documented here
		// because it is what the doc now claims, not because it is desirable —
		// tightening markerRe would make this arm red and is the signal that
		// the doc needs rewriting with it.
		{"content wrapper carrying the attribute matches", `<ctxloom-context kind="harp" name="wrapper-harp">`, "wrapper-harp"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Find(tc.in); got != tc.want {
				t.Fatalf("Find(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}
