// Package agent is ctxloom's engine-agnostic agent core. It holds the contract
// and machinery every agent adapter shares: the settings-writing reconciler, the
// owner predicate, and the canonical marshaller. The per-engine wire formats live
// in the agent packages (claude/antigravity); nothing here is engine-specific.
package agent

import "encoding/json"

// CanonicalJSON marshals v to the canonical on-disk form: keys sorted
// recursively, two-space indented, with a trailing newline. Structs are
// flattened to maps via a JSON round-trip so field-declaration order never
// leaks into the file — the reason ctxloom and ltk used to fight over key order
// when each rewrote the same .claude/settings.json (ctxloom emitted struct
// order, ltk sorted map order). With both on this, whichever writes last
// produces identical bytes.
func CanonicalJSON(v any) ([]byte, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, err
	}
	// Decoding into `any` turns every object into a map[string]any, which
	// json.Marshal then emits with keys sorted — recursively, at every depth.
	var generic any
	if err := json.Unmarshal(raw, &generic); err != nil {
		return nil, err
	}
	out, err := json.MarshalIndent(generic, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}
