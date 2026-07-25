package main

import (
	"bytes"
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

// The shipped, embedded sample must be exactly what the doc assembles to — the
// same invariant the lefthook -check enforces, guarded here in the unit suite.
func TestEmbeddedSampleMatchesDoc(t *testing.T) {
	md, err := readUp(source)
	if err != nil {
		t.Skipf("doc not found from test cwd: %v", err)
	}
	want, err := assemble(md, minDefaultRules)
	if err != nil {
		t.Fatal(err)
	}
	have, err := readUp(generated)
	if err != nil {
		t.Fatalf("read generated: %v", err)
	}
	if !bytes.Equal(have, want) {
		t.Errorf("%s is out of sync with %s — run `just defaults`", generated, source)
	}
}

// U077-F01: assemble validated the BLOCK count, never the RULE count. A doc
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
