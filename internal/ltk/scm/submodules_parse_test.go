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

// git-config values are not raw line remainders: `;` and `#` begin a comment
// outside quotes, and a quoted value carries neither its quotes nor the comment
// meaning of the characters inside it. Taking the remainder literally is a
// silent mis-parse in the dangerous direction — ExpandSubmodules turns
// `"libs/quoted"` into the deny pattern `"libs/quoted"/`, which matches no real
// path, so the submodule the operator declared is guarded by nothing at all and
// no diagnostic says so.
func TestSubmodulePaths_StripsCommentsAndQuotes(t *testing.T) {
	const doc = `[submodule "a"]
	path = libs/semi ; a trailing comment
[submodule "b"]
	path = libs/hash # another comment
[submodule "c"]
	path = "libs/quoted"
[submodule "d"]
	path = "libs/with space"
[submodule "e"]
	path = "libs/keeps;hash#chars"
[submodule "f"]
	path = "libs/esc\"quote"
`
	// The fixture must be hostile: every one of these values differs from the
	// raw, whitespace-trimmed line remainder the parser used to emit.
	raw := []string{
		"libs/semi ; a trailing comment",
		"libs/hash # another comment",
		`"libs/quoted"`,
		`"libs/with space"`,
		`"libs/keeps;hash#chars"`,
		`"libs/esc\"quote"`,
	}
	for _, r := range raw {
		if !strings.Contains(doc, "= "+r+"\n") {
			t.Fatalf("fixture is not hostile: expected a raw value %q in the document", r)
		}
	}

	got := parsePaths(t, doc)
	want := []string{
		"libs/semi",
		"libs/hash",
		"libs/quoted",
		"libs/with space",
		"libs/keeps;hash#chars",
		`libs/esc"quote`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("SubmodulePaths = %#v, want %#v", got, want)
	}
}

// A value that is nothing but a comment is empty, not a submodule path — the
// same emptiness check the bare form already applied.
func TestSubmodulePaths_CommentOnlyValueIsNotAPath(t *testing.T) {
	const doc = `[submodule "a"]
	path = ; nothing here
[submodule "b"]
	path = libs/real
`
	if got, want := parsePaths(t, doc), []string{"libs/real"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("SubmodulePaths = %v, want %v", got, want)
	}
}

// An empty startDir is a caller that does not know where it is. filepath.Clean
// turns it into ".", whose parent is itself, so the walk ends on its first
// iteration and answers nil — "this repository declares no submodules" — which
// is the one answer this function must never invent. It is the same collapse
// that was removed from the read path: the caller has to be able to tell an
// absence from a failure to find out.
func TestSubmodulePaths_EmptyStartDirIsAnError(t *testing.T) {
	fs := afero.NewMemMapFs()
	if err := afero.WriteFile(fs, "/repo/.gitmodules", []byte("[submodule \"x\"]\n\tpath = x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	// The fixture must be hostile: this filesystem really does declare a
	// submodule, so a nil answer is a loss and not an honest absence.
	if got, err := SubmodulePaths(fs, "/repo"); err != nil || !reflect.DeepEqual(got, []string{"x"}) {
		t.Fatalf("fixture is not hostile: /repo = %v, %v; want [x], nil", got, err)
	}

	got, err := SubmodulePaths(fs, "")
	if err == nil {
		t.Fatalf("an empty startDir was silently accepted: got %v, want an error", got)
	}
	if got != nil {
		t.Fatalf("paths returned alongside an error: %v", got)
	}
}

// The repo boundary is "a .git that git itself would accept", not "an entry
// called .git exists". A `.git` FILE is a boundary only when it is a gitfile
// pointer (a submodule working tree or linked worktree); anything else named
// .git is a broken repo by git's own rules, and treating it as a root makes
// SubmodulePaths answer "this repo declares no submodules" — the same fail-open
// nil this function was fixed to stop returning for read failures.
func TestSubmodulePaths_StrayGitFileIsNotARepoRoot(t *testing.T) {
	newFS := func(t *testing.T) afero.Fs {
		t.Helper()
		fs := afero.NewMemMapFs()
		if err := fs.MkdirAll("/outer/.git", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := afero.WriteFile(fs, "/outer/.gitmodules", []byte("[submodule \"x\"]\n\tpath = x\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := fs.MkdirAll("/outer/sub", 0o755); err != nil {
			t.Fatal(err)
		}
		return fs
	}

	// The fixture must be hostile: without the stray entry the walk reaches the
	// outer repo's .gitmodules, so anything else is a truncation caused by it.
	base := newFS(t)
	if got, err := SubmodulePaths(base, "/outer/sub"); err != nil || !reflect.DeepEqual(got, []string{"x"}) {
		t.Fatalf("fixture is not hostile: baseline walk = %v, %v; want [x], nil", got, err)
	}

	t.Run("a .git file that is not a gitfile pointer", func(t *testing.T) {
		fs := newFS(t)
		if err := afero.WriteFile(fs, "/outer/sub/.git", []byte("scratch notes, not a repo\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := SubmodulePaths(fs, "/outer/sub")
		if err == nil {
			t.Fatalf("a stray .git file was silently accepted as a repo root: got %v, want an error", got)
		}
		if got != nil {
			t.Fatalf("paths returned alongside an error: %v", got)
		}
	})

	t.Run("a real gitfile pointer is still a boundary", func(t *testing.T) {
		fs := newFS(t)
		if err := afero.WriteFile(fs, "/outer/sub/.git", []byte("gitdir: /outer/.git/modules/sub\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := SubmodulePaths(fs, "/outer/sub")
		if err != nil {
			t.Fatalf("SubmodulePaths: %v", err)
		}
		if got != nil {
			t.Fatalf("the superproject's submodules leaked past a gitfile boundary: %v", got)
		}
	})
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
