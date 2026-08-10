package agents

import (
	"strings"
	"testing"
)

// `coordinator:` was REMOVED, not renamed: whether a run may delegate is now
// decided by its position in the tree — its depth against delegation.depth —
// rather than declared per binding.
//
// It is refused rather than ignored because real configs carry it. When the
// leaf tool-gate first shipped, every binding that needed to delegate had to be
// marked `coordinator: true` or its children lost the delegation tools, so the
// flag is written into this repo's own config and into published bundles.
// Silently dropping it would leave a binding authored to delegate quietly
// unable to, reported as success — the shape this codebase treats as its
// characteristic bug.

func TestParseAgent_RefusesTheRemovedCoordinatorKey(t *testing.T) {
	_, err := ParseAgent([]byte("profiles: [dev]\nllm: claude-fast\ncoordinator: true\n"))
	if err == nil {
		t.Fatal("an agent using the removed `coordinator:` key must be refused, not silently ignored")
	}
	if !strings.Contains(err.Error(), "delegation.depth") {
		t.Errorf("the refusal must name what replaced the flag (delegation.depth), "+
			"or the reader cannot tell whether their delegation still works; got: %v", err)
	}
}

// `coordinator: false` must be refused too. It is the value a reader is most
// likely to believe is a harmless no-op — it looks like it is asking for the
// default — so accepting it would teach exactly the wrong thing about whether
// the key still means anything.
func TestParseAgent_RefusesTheRemovedCoordinatorKeyWhenFalse(t *testing.T) {
	_, err := ParseAgent([]byte("profiles: [dev]\nllm: claude-fast\ncoordinator: false\n"))
	if err == nil {
		t.Fatal("`coordinator: false` must be refused as well: the key is gone, not defaulted")
	}
}

// The control. Without it this file would still pass with ParseAgent refusing
// every agent for any reason, which would prove nothing about the key.
func TestParseAgent_AcceptsABindingWithoutTheRemovedKey(t *testing.T) {
	got, err := ParseAgent([]byte("profiles: [dev]\nllm: claude-fast\n"))
	if err != nil {
		t.Fatalf("a binding carrying no removed key must parse: %v", err)
	}
	if got.LLM != "claude-fast" {
		t.Errorf("LLM = %q, want %q", got.LLM, "claude-fast")
	}
}
