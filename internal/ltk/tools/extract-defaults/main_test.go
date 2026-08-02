package main

import (
	"bytes"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAssembleConcatenatesBlocksInOrder(t *testing.T) {
	md := []byte("# Doc\n\nintro prose\n\n" +
		"```yaml\nversion: 1\nrules:\n```\n\nrationale for rule A\n\n" +
		"```yaml\n  - id: a\n    match: { command: [go, test] }\n    message: x\n```\n\n" +
		"more prose\n\n" +
		"```yaml\n  - id: b\n    match: { command: [git, tag] }\n    message: y\n```\n")

	out, err := assemble(md, 1)
	if err != nil {
		t.Fatal(err)
	}
	s := string(out)
	if !strings.HasPrefix(s, header) {
		t.Error("output must start with the generated-file header")
	}
	// Prose is excluded; rules appear in document order. (Check phrases that only
	// occur in the doc's prose, not words the header legitimately contains.)
	if strings.Contains(s, "rationale for rule") || strings.Contains(s, "intro prose") {
		t.Error("non-yaml prose leaked into the output")
	}
	ia, ib := strings.Index(s, "id: a"), strings.Index(s, "id: b")
	if ia < 0 || ib < 0 || ia > ib {
		t.Errorf("rules out of order or missing: ia=%d ib=%d", ia, ib)
	}
}

func TestAssembleRejectsNoBlocks(t *testing.T) {
	if _, err := assemble([]byte("# Doc with no yaml fences\n"), 1); err == nil {
		t.Error("expected an error when no ```yaml blocks are present")
	}
}

func TestAssembleRejectsInvalidRuleSet(t *testing.T) {
	// A block that parses as YAML but violates the rule schema (duplicate id).
	md := []byte("```yaml\nversion: 1\nrules:\n```\n" +
		"```yaml\n  - id: dup\n    match: { command: a }\n```\n" +
		"```yaml\n  - id: dup\n    match: { command: b }\n```\n")
	if _, err := assemble(md, 1); err == nil {
		t.Error("expected assemble to reject a rule set that fails validation")
	}
}

// The shipped, embedded sample must be exactly what the doc assembles to.
//
// THIS TEST IS THE DRIFT GATE. The -check flag exists to enforce the same
// invariant from a hook, but nothing invokes it: the justfile runs the no-arg
// form, lefthook.yml has no extract-defaults entry, and no CI workflow
// mentions it. So if this test does not run, or skips itself, the invariant is
// unguarded — which is why it resolves the doc from a compiled-in source path
// and fails rather than skipping when it cannot find one.
func TestEmbeddedSampleMatchesDoc(t *testing.T) {
	md := readModuleFile(t, source)
	want, err := assemble(md, minDefaultRules)
	if err != nil {
		t.Fatal(err)
	}
	have := readModuleFile(t, generated)
	if !bytes.Equal(have, want) {
		t.Errorf("%s is out of sync with %s — run `just defaults`", generated, source)
	}
}

// assemble validated the BLOCK count, never the RULE count. A doc
// whose yaml fences carry prose, comments or an empty `rules:` key assembles to
// a rule-free config, and rules.Parse accepts that happily (its decoder
// tolerates io.EOF and normalizeAndValidate has no minimum). The result is
// embedded into the ltk binary and written into a user's project on `manage
// install`, so an empty default rule set means ltk installs a guard that
// permits everything — silently, with the generator reporting success.
func TestAssembleRejectsARuleFreeDocument(t *testing.T) {
	// Well-formed fences, valid YAML, zero rules.
	md := []byte("# Doc\n\n```yaml\nversion: 1\nrules:\n```\n\n```yaml\n# only a comment\n```\n")
	out, err := assemble(md, 1)
	if err == nil {
		t.Fatalf("expected an error for a document that assembles to zero rules, got %d bytes", len(out))
	}
	if !strings.Contains(err.Error(), "no rules") {
		t.Errorf("the error must name the real problem, got: %v", err)
	}
}

// The doc ships 17 fenced blocks and 16 rules. A change that silently drops most
// of them is as invisible as dropping all of them, so the generator asserts a
// floor rather than only a non-zero count.
func TestAssembleRejectsAnImplausiblyShortRuleSet(t *testing.T) {
	md := []byte("```yaml\nversion: 1\nrules:\n```\n" +
		"```yaml\n  - id: only-one\n    match: { command: [go, test] }\n    message: x\n```\n")
	if _, err := assemble(md, minDefaultRules); err == nil {
		t.Error("expected an error for a rule set far below the shipped floor")
	}
}

// Every fenced block in DEFAULTS.md is a rule block — the doc is the source of
// truth for the shipped guard, not a mixed tutorial. So a fence that does not
// open with exactly ```yaml is an authoring slip, and skipping it drops a
// default rule out of the binary ltk installs into a user's project.
//
// Nothing downstream catches that. The rule-count floor only fires on a large
// loss, and the drift check cannot fire at all: it compares the generated file
// against a re-extraction of the SAME document with the SAME reader, so both
// sides drop the same block and agree perfectly on a rule set that is missing
// one. Refuse instead.
func TestAssembleRejectsAFenceThatIsNotYaml(t *testing.T) {
	good := "```yaml\nversion: 1\nrules:\n  - id: a\n    match: { command: [git, push] }\n    action: deny\n    message: m\n```\n"
	for name, info := range map[string]string{
		"lowercase yml":      "yml",
		"uppercase":          "YAML",
		"info attributes":    "yaml title=\"rules\"",
		"no info string":     "",
		"leading whitespace": "  yaml",
	} {
		t.Run(name, func(t *testing.T) {
			md := []byte("# Doc\n\n" + good + "\n```" + info + "\nversion: 1\nrules:\n  - id: b\n    match: { command: [rm] }\n    action: deny\n    message: m\n```\n")

			// The fixture must be hostile: the second block carries a real rule
			// that the reader is supposed to lose.
			if !bytes.Contains(md, []byte("id: b")) {
				t.Fatal("fixture is not hostile: no second rule to drop")
			}
			if _, err := assemble(md, 1); err == nil {
				t.Fatalf("a ```%s fence was silently skipped; the rule in it never reaches the shipped defaults", info)
			}
		})
	}
}

// An unterminated block is the other way to lose content silently: the reader
// used to need a closing fence to emit anything at all, so a missing one threw
// the block away.
func TestAssembleRejectsAnUnterminatedBlock(t *testing.T) {
	md := []byte("# Doc\n\n```yaml\nversion: 1\nrules:\n  - id: a\n    match: { command: [git, push] }\n    action: deny\n    message: m\n")
	if _, err := assemble(md, 1); err == nil {
		t.Fatal("an unterminated ```yaml block was accepted")
	}
}

// -check answered "out of sync — run `just defaults`" for every failure,
// including ones where nothing had drifted at all. A permission-denied read is
// not drift: regenerating cannot fix it, and the operator is sent to edit a
// document that was never the cause. An ABSENT file is different again — that
// one really is fixed by regenerating — so all three are kept apart.
func TestCheckDriftSeparatesUnreadableFromDrifted(t *testing.T) {
	want := []byte("a\n")

	t.Run("in sync", func(t *testing.T) {
		if err := checkDrift(want, nil, want); err != nil {
			t.Fatalf("identical bytes reported a problem: %v", err)
		}
	})

	t.Run("genuinely drifted", func(t *testing.T) {
		err := checkDrift([]byte("b\n"), nil, want)
		if err == nil || !strings.Contains(err.Error(), "out of sync") {
			t.Fatalf("drift not reported as drift: %v", err)
		}
	})

	t.Run("absent", func(t *testing.T) {
		err := checkDrift(nil, fs.ErrNotExist, want)
		if err == nil {
			t.Fatal("a missing generated file was accepted as in sync")
		}
		if strings.Contains(err.Error(), "out of sync") {
			t.Fatalf("a missing file was diagnosed as drift: %v", err)
		}
		if !strings.Contains(err.Error(), "does not exist") {
			t.Fatalf("the absence is not named: %v", err)
		}
	})

	t.Run("unreadable", func(t *testing.T) {
		err := checkDrift(nil, fs.ErrPermission, want)
		if err == nil {
			t.Fatal("an unreadable generated file was accepted as in sync")
		}
		if strings.Contains(err.Error(), "out of sync") {
			t.Fatalf("an unreadable file was diagnosed as drift: %v", err)
		}
		if !errors.Is(err, fs.ErrPermission) {
			t.Fatalf("the underlying read failure was dropped: %v", err)
		}
	})
}

// source and generated are module-root-relative, and the tool used to hand
// them straight to os.ReadFile — so "which directory you ran this from" was
// part of its correctness, an unwritten precondition that `just defaults`
// satisfies and a developer standing in any subdirectory does not. Resolving
// them against the module root removes the precondition instead of documenting
// it.
//
// This pins the resolution itself; main's own use of it ends in os.Exit and is
// not observable from a test.
func TestModuleRootIsFoundFromAnySubdirectory(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(root, "internal", "ltk", "tools", "extract-defaults")
	if err := os.MkdirAll(nested, 0o755); err != nil {
		t.Fatal(err)
	}

	// The fixture must be hostile: the nested directory must NOT itself carry
	// a go.mod, or the walk never happens.
	if _, err := os.Stat(filepath.Join(nested, "go.mod")); err == nil {
		t.Fatal("fixture is not hostile: the nested dir has its own go.mod")
	}

	for _, start := range []string{root, nested} {
		got, err := moduleRoot(start)
		if err != nil {
			t.Fatalf("moduleRoot(%q): %v", start, err)
		}
		// t.TempDir can hand back a symlinked path (/tmp -> /private/tmp on
		// darwin), so compare what the walk actually traversed.
		if filepath.Clean(got) != filepath.Clean(root) {
			t.Errorf("moduleRoot(%q) = %q, want %q", start, got, root)
		}
	}
}

// Outside a module the tool must say so, not read whatever happens to sit in
// the working directory.
func TestModuleRootReportsWhenThereIsNoModule(t *testing.T) {
	_, err := moduleRoot(t.TempDir())
	if err == nil {
		t.Fatal("a directory with no go.mod above it was accepted")
	}
	if !strings.Contains(err.Error(), "go.mod") {
		t.Fatalf("the error does not name what is missing: %v", err)
	}
}
