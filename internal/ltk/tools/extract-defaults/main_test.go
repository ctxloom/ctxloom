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

	out, err := assemble(md)
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
	if _, err := assemble([]byte("# Doc with no yaml fences\n")); err == nil {
		t.Error("expected an error when no ```yaml blocks are present")
	}
}

func TestAssembleRejectsInvalidRuleSet(t *testing.T) {
	// A block that parses as YAML but violates the rule schema (duplicate id).
	md := []byte("```yaml\nversion: 1\nrules:\n```\n" +
		"```yaml\n  - id: dup\n    match: { command: a }\n```\n" +
		"```yaml\n  - id: dup\n    match: { command: b }\n```\n")
	if _, err := assemble(md); err == nil {
		t.Error("expected assemble to reject a rule set that fails validation")
	}
}

// The shipped, embedded sample must be exactly what the doc assembles to — the
// same invariant the lefthook -check enforces, guarded here in the unit suite.
func TestEmbeddedSampleMatchesDoc(t *testing.T) {
	md, err := readUp("docs/DEFAULTS.md")
	if err != nil {
		t.Skipf("doc not found from test cwd: %v", err)
	}
	want, err := assemble(md)
	if err != nil {
		t.Fatal(err)
	}
	have, err := readUp("cmd/ltk/sample.ltk.yaml")
	if err != nil {
		t.Fatalf("read generated: %v", err)
	}
	if !bytes.Equal(have, want) {
		t.Error("cmd/ltk/sample.ltk.yaml is out of sync with docs/DEFAULTS.md — run `just defaults`")
	}
}
