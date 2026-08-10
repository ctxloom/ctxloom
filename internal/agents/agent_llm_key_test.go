package agents

import (
	"strings"
	"testing"
)

// The field holds an LLM CONFIG LABEL, and is spelled `llm` to say so.
//
// GLOSSARY.md reserves "engine" for what the runner drives (claude-code,
// codex, kiro). What an agent binding actually carries is a label into
// `llm.configs`, which names an engine AND a model AND its credentials — the
// live value `claude-fast` is a label with no engine of that name. Calling it
// `engine` made the binding claim to select a thing it does not select, and
// `--llm` is already the flag that sets it.

func TestParseAgent_ReadsTheLLMLabel(t *testing.T) {
	got, err := ParseAgent([]byte("profiles: [dev]\nllm: claude-fast\n"))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.LLM != "claude-fast" {
		t.Errorf("LLM = %q, want %q", got.LLM, "claude-fast")
	}
}

// The retired key REFUSES, naming the current spelling.
//
// KnownFields(true) already rejects it, but with yaml's own "field engine not
// found" — which tells a reader the key is wrong and not what to type instead.
// A rename that leaves people guessing has moved the cost rather than paid it.
func TestParseAgent_RefusesTheRetiredEngineKey(t *testing.T) {
	_, err := ParseAgent([]byte("profiles: [dev]\nengine: claude-code\n"))
	if err == nil {
		t.Fatal("an agent using the retired `engine:` key must be refused")
	}
	if !strings.Contains(err.Error(), "llm") {
		t.Errorf("the refusal must name the current spelling `llm`; got: %v", err)
	}
}
