package memory

import (
	"context"
	"fmt"
	"strings"
)

// Runner drives ONE turn of a resolved distiller session: the whole prompt
// in, the answer out. The caller resolves the launch (a real session: a
// harp, an endpoint, the managed surfaces) and hands the turn here; this
// package never composes a launch or names an engine.
type Runner func(ctx context.Context, prompt string) (string, error)

// Distill executes one distillation turn: systemPrompt as the instruction
// ahead of payload as the material it works on. The session distillation,
// the per-result finding repair and the premise author all go through here
// so the prompt shape stays in one place.
//
// payload is the ALREADY-ENVELOPED material — the caller wraps it in whatever
// element names what it is (<session_log> for a transcript, <fragment> for a
// fragment body), because only the caller knows what the material is.
func Distill(ctx context.Context, run Runner, systemPrompt, payload string) (string, error) {
	if run == nil {
		return "", fmt.Errorf("distill: no runner — the caller resolves the distiller's launch and hands its turn here")
	}
	out, err := run(ctx, fmt.Sprintf("%s\n\n%s", systemPrompt, payload))
	if err != nil {
		return "", err
	}
	// Exit 0 with nothing on stdout is a FAILED call, not an empty result.
	// Counted as success it lands at the caller as "", which for the compactor
	// meant an empty result written straight over a previously good essence.md.
	out = strings.TrimSpace(out)
	if out == "" {
		return "", fmt.Errorf("LLM produced no output")
	}
	return out, nil
}
