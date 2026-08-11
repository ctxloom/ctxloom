// Package agent is ctxloom's engine-agnostic agent core. It holds the contract
// and machinery every agent adapter shares: the settings-writing reconciler, the
// owner predicate, and the canonical marshaller. The per-engine wire formats live
// in the agent packages (claude); nothing here is engine-specific.
package agent

import (
	"bytes"
	"encoding/json"
)

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
	//
	// UseNumber is load-bearing, not a tuning knob: canonicalising must be
	// value-preserving. A plain generic decode lands every number in float64,
	// so re-encoding rewrites any literal float64 cannot hold exactly — an
	// int64 past 2^53 comes back rounded (1234567890123456789 →
	// 1234567890123456800) and a long decimal comes back truncated, with no
	// error, in a file the user authored. json.Number keeps the original
	// literal and the encoder re-emits it verbatim, so the numbers on disk are
	// the numbers that went in. It also stops rejecting magnitudes outside
	// float64's range (1e1000): those now survive too.
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var generic any
	if err := dec.Decode(&generic); err != nil {
		return nil, err
	}
	out, err := json.MarshalIndent(generic, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}
