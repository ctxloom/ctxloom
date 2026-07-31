package scm

import (
	"reflect"
	"strings"
	"testing"

	"github.com/spf13/afero"
)

// countPathKeys counts the lines a stanza-blind scan would accept as a
// `path = …` key. It exists so a fixture can assert it is genuinely hostile —
// that the document really does carry decoy `path` keys the parser must reject —
// rather than passing because the fixture never exercised the defect.
func countPathKeys(doc string) int {
	n := 0
	for _, line := range strings.Split(doc, "\n") {
		key, _, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && strings.TrimSpace(key) == "path" {
			n++
		}
	}
	return n
}

func parsePaths(t *testing.T, doc string) []string {
	t.Helper()
	fs := afero.NewMemMapFs()
	if err := afero.WriteFile(fs, "/repo/.gitmodules", []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := SubmodulePaths(fs, "/repo")
	if err != nil {
		t.Fatalf("SubmodulePaths: %v", err)
	}
	return got
}

// A `path` key is a submodule path only inside a `[submodule …]` stanza.
// .gitmodules is git-config, and git itself reads only `submodule.<name>.path`;
// any other section's `path` key belongs to that section. A stanza-blind scan
// turns those into extra "@submodules" deny patterns, so a rule the operator
// wrote to guard the repo's submodules silently guards unrelated directories
// too — a rule firing on paths nobody asked it to cover.
func TestSubmodulePaths_IgnoresPathKeysOutsideSubmoduleStanzas(t *testing.T) {
	const doc = `[core]
	path = etc/decoy-core
[remote "origin"]
	url = https://example.com/x.git
	path = mirrors/decoy-remote
[submodule "libs/foo"]
	path = libs/foo
	url = https://example.com/foo.git
[SUBMODULE "vendor/bar"]
	path = vendor/bar
`
	// The fixture must be hostile: three keys look like `path = …` to a
	// stanza-blind scan, and only two of them are submodule paths.
	if n := countPathKeys(doc); n != 4 {
		t.Fatalf("fixture is not hostile: %d bare `path =` keys, want 4", n)
	}

	got := parsePaths(t, doc)
	want := []string{"libs/foo", "vendor/bar"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SubmodulePaths = %v, want %v", got, want)
	}
}

// git-config permits a variable on the same line as the section header that
// introduces it, so `[submodule "x"] path = x` is a legal single-line stanza.
// Dropping it would lose a real submodule — the fail-open direction, where the
// expanded rule quietly covers one fewer directory than the operator declared.
func TestSubmodulePaths_SectionHeaderAndKeyOnOneLine(t *testing.T) {
	const doc = `[submodule "libs/foo"] path = libs/foo
[core] path = etc/decoy
`
	if got, want := parsePaths(t, doc), []string{"libs/foo"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("SubmodulePaths = %v, want %v", got, want)
	}
}
