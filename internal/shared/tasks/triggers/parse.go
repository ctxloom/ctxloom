package triggers

import (
	"encoding/json"
	"fmt"
	"strings"
)

// ParseVerdicts parses the model's batch-triage response into verdicts.
// There is no schema-constrained output anywhere in this codebase (see
// parseLLMFrontmatter in internal/memory/compactor.go for the established
// prompt-in/text-out precedent) — the model is ASKED for strict JSON, but the
// response may still carry a markdown code fence or a leading sentence of
// prose, so both are stripped defensively before unmarshaling.
//
// It never panics, and it never returns a partial/best-effort result: any
// failure to extract a well-formed verdict array is an error, because a
// caller that silently accepted a garbled verdict risks misreading a
// truncated "cannot-determine" as "fired" — worse than surfacing the failure
// so the caller can retry or degrade explicitly.
func ParseVerdicts(raw string) ([]Verdict, error) {
	body := extractJSONArray(raw)
	if body == "" {
		return nil, fmt.Errorf("triggers: no JSON array found in model output")
	}

	var out []Verdict
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		return nil, fmt.Errorf("triggers: parse verdict JSON: %w", err)
	}

	for i, v := range out {
		if strings.TrimSpace(v.HarpID) == "" {
			return nil, fmt.Errorf("triggers: verdict %d has no harp_id", i)
		}
		if !v.Outcome.Valid() {
			return nil, fmt.Errorf("triggers: verdict %d (%s) has an unrecognized outcome %q", i, v.HarpID, v.Outcome)
		}
		// Cached is caller-stamped only, never model-supplied (see Verdict's
		// doc comment) — reset it regardless of what the response carried.
		out[i].Cached = false
	}
	return out, nil
}

// extractJSONArray isolates the JSON array in raw, tolerating a wrapping
// markdown code fence and stray prose around it. Returns "" when no
// array-shaped substring can be found; every index used is bounds-checked, so
// this never panics regardless of input.
func extractJSONArray(raw string) string {
	s := stripCodeFence(strings.TrimSpace(raw))
	start := strings.Index(s, "[")
	end := strings.LastIndex(s, "]")
	if start == -1 || end == -1 || end < start {
		return ""
	}
	return s[start : end+1]
}

// stripCodeFence removes a wrapping ``` or ```json fence, when present. Any
// fence shape that doesn't match cleanly is left as-is — extractJSONArray's
// bracket scan still finds the array inside it.
func stripCodeFence(s string) string {
	if !strings.HasPrefix(s, "```") {
		return s
	}
	rest := strings.TrimPrefix(s, "```")
	if nl := strings.Index(rest, "\n"); nl != -1 {
		// Drop the fence-opener line itself (e.g. a bare "json" tag).
		rest = rest[nl+1:]
	}
	if end := strings.LastIndex(rest, "```"); end != -1 {
		rest = rest[:end]
	}
	return strings.TrimSpace(rest)
}
