package resources

import (
	"testing"
	"testing/fstest"
)

// listingFixture exercises every shape the two builtin listers have to
// classify: a normal match, a non-matching extension, a directory whose name
// ends in the extension, a name that is the extension without its dot, and a
// file whose ENTIRE name is the extension. That last one is the interesting
// case — the index arithmetic both listers used ("len(name) > 3 &&
// name[len-3:] == \".md\"") rejects it, while a naive
// strings.HasSuffix/TrimSuffix rewrite accepts it and yields an EMPTY name.
func listingFixture(ext string) fstest.MapFS {
	return fstest.MapFS{
		"d/real" + ext:            {Data: []byte("x")},
		"d/second" + ext:          {Data: []byte("y")},
		"d/other.txt":             {Data: []byte("x")},
		"d/dir" + ext + "/in.txt": {Data: []byte("x")},
		"d/" + ext[1:]:            {Data: []byte("x")},
		"d/" + ext:                {Data: []byte("x")},
	}
}

func TestListEmbeddedNames(t *testing.T) {
	for _, ext := range []string{".md", ".yaml"} {
		t.Run(ext, func(t *testing.T) {
			got, err := listEmbeddedNames(listingFixture(ext), "d", ext)
			if err != nil {
				t.Fatalf("listEmbeddedNames: %v", err)
			}
			want := []string{"real", "second"}
			if len(got) != len(want) {
				t.Fatalf("got %v, want %v", got, want)
			}
			for i := range want {
				if got[i] != want[i] {
					t.Fatalf("got %v, want %v", got, want)
				}
			}
			for _, n := range got {
				if n == "" {
					t.Errorf("listEmbeddedNames returned an EMPTY name from %v; a file whose whole name is %q is not a named item", got, ext)
				}
			}
		})
	}
}

// TestListEmbeddedNamesRefusesEmptyResult pins that an enumeration producing
// NOTHING is reported rather than returned as an empty set. These directories
// are embedded at build time, so zero names never means "the user has none" —
// it means the binary shipped without content it is supposed to carry. Every
// caller (internal/lm/backends.builtinCommands, internal/config's three
// builtin-bundle resolvers, config.companion listing) already warns and
// degrades on an error and did nothing at all on the silent nil.
func TestListEmbeddedNamesRefusesEmptyResult(t *testing.T) {
	cases := map[string]fstest.MapFS{
		"present but empty":  {"d/.keep": {Data: []byte("")}},
		"extension mismatch": {"d/a.txt": {Data: []byte("x")}, "d/b.yml": {Data: []byte("x")}},
		"only subdirectories": {
			"d/sub/a.md": {Data: []byte("x")},
		},
	}
	for name, fsys := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := listEmbeddedNames(fsys, "d", ".md")
			if err == nil {
				t.Fatalf("listEmbeddedNames returned (%v, nil); an embedded directory that yields no names is a build defect and must be reported", got)
			}
			if got != nil {
				t.Errorf("got %v alongside the error, want nil", got)
			}
		})
	}
}

// TestGetPromptTextRefusesEmptyFile pins that a present-but-empty embedded
// prompt is an error rather than an empty string. MustGetPromptText documents
// itself as panicking "rather than shipping an empty prompt"; without this it
// happily returned "" and shipped exactly that, wiring a zero-length system
// prompt into a live session with no signal anywhere.
func TestGetPromptTextRefusesEmptyFile(t *testing.T) {
	for name, body := range map[string]string{
		"zero bytes":      "",
		"newlines only":   "\n\n\n",
		"whitespace only": "   \n\t\n",
	} {
		t.Run(name, func(t *testing.T) {
			fsys := fstest.MapFS{"prompts/p.md": {Data: []byte(body)}}
			got, err := getPromptText(fsys, "p")
			if err == nil {
				t.Fatalf("getPromptText returned (%q, nil) for an empty prompt file", got)
			}
			if got != "" {
				t.Errorf("got %q alongside the error, want \"\"", got)
			}
		})
	}
}
