// Package llmturn is the one prompt shape every internal LLM one-shot turn
// takes: an instruction ahead of the material it works on, sent through a
// turn the caller has already resolved.
package llmturn

import (
	"context"
	"fmt"
	"strings"
)

// Runner drives ONE turn of a resolved internal one-shot: the whole
// prompt in, the answer out. operations.OneShotSession.Turn is one. The caller resolves
// the launch (a real session: a harp, an endpoint, the managed surfaces) and
// hands the turn here; RunPrompt never composes a launch or names an engine.
type Runner func(ctx context.Context, prompt string) (string, error)

// RunPrompt executes one LLM turn: systemPrompt as the instruction ahead of
// payload as the material it works on. The premise author and critique and
// the task-trigger triage all go through here so the prompt shape stays in
// one place.
//
// payload is the ALREADY-ENVELOPED material — the caller wraps it in whatever
// element names what it is (<fragment> for a fragment body), because only the
// caller knows what the material is.
func RunPrompt(ctx context.Context, run Runner, systemPrompt, payload string) (string, error) {
	if run == nil {
		return "", fmt.Errorf("run prompt: no runner — the caller resolves the one-shot's launch and hands its turn here")
	}
	out, err := run(ctx, fmt.Sprintf("%s\n\n%s", systemPrompt, payload))
	if err != nil {
		return "", err
	}
	// Exit 0 with nothing on stdout is a FAILED call, not an empty result:
	// counted as success it would land at the caller as "", indistinguishable
	// from an answer.
	out = strings.TrimSpace(out)
	if out == "" {
		return "", fmt.Errorf("LLM produced no output")
	}
	return out, nil
}
