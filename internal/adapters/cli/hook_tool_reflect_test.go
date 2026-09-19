package cli

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/ctxloom/ctxloom/internal/claude"
)

// payloadWithResponseBytes builds a PostToolUse payload whose tool_response
// encodes to at least n bytes, so a test can sit either side of a threshold
// without hard-coding a magic blob.
func payloadWithResponseBytes(t *testing.T, n int) []byte {
	t.Helper()
	raw, err := json.Marshal(strings.Repeat("x", n))
	if err != nil {
		t.Fatalf("marshal response body: %v", err)
	}
	p, err := json.Marshal(map[string]any{
		"session_id":    "s1",
		"tool_name":     "Bash",
		"tool_response": json.RawMessage(raw),
	})
	if err != nil {
		t.Fatalf("marshal payload: %v", err)
	}
	return p
}

// TestBuildToolReflectOutput_FiresOnlyAtOrAboveThreshold pins BOTH sides of the
// threshold in one test. Asserting only that a large result fires would be
// satisfied by a hook that fires unconditionally -- which is the expensive
// failure here, since it would inject on every tool call in the session.
func TestBuildToolReflectOutput_FiresOnlyAtOrAboveThreshold(t *testing.T) {
	const threshold = 2048

	small := buildToolReflectOutput(payloadWithResponseBytes(t, 100), threshold)
	if small.HookSpecificOutput != nil {
		t.Fatalf("hook fired below threshold: %+v", small.HookSpecificOutput)
	}

	large := buildToolReflectOutput(payloadWithResponseBytes(t, threshold*2), threshold)
	if large.HookSpecificOutput == nil {
		t.Fatal("hook stayed silent on a result well above the threshold")
	}
	if large.HookSpecificOutput.AdditionalContext != ToolReflectReminder {
		t.Fatalf("injected text is not the reminder: %q", large.HookSpecificOutput.AdditionalContext)
	}
	if large.HookSpecificOutput.HookEventName != claude.HookEventPostToolUse {
		t.Fatalf("wrong hookEventName %q; the host routes on this",
			large.HookSpecificOutput.HookEventName)
	}
}

// TestBuildToolReflectOutput_SilentOnUndecodablePayload pins that a payload the
// hook cannot parse produces silence, not a reminder. A hook that fired on
// everything it failed to understand would be loudest where it knew least.
func TestBuildToolReflectOutput_SilentOnUndecodablePayload(t *testing.T) {
	got := buildToolReflectOutput([]byte("this is not json"), 1)
	if got.HookSpecificOutput != nil {
		t.Fatalf("hook fired on an undecodable payload: %+v", got.HookSpecificOutput)
	}
}

// TestBuildToolReflectOutput_NonPositiveThresholdDisables pins the disable
// path: a threshold of zero or less must never inject, even on a huge result.
func TestBuildToolReflectOutput_NonPositiveThresholdDisables(t *testing.T) {
	for _, threshold := range []int{0, -1} {
		t.Run(fmt.Sprintf("threshold=%d", threshold), func(t *testing.T) {
			got := buildToolReflectOutput(payloadWithResponseBytes(t, 100_000), threshold)
			if got.HookSpecificOutput != nil {
				t.Fatalf("hook fired with threshold %d: %+v", threshold, got.HookSpecificOutput)
			}
		})
	}
}

// TestBuildToolReflectOutput_SilentOutputEncodesEmpty pins that staying silent
// still produces a valid, EMPTY envelope on the wire. The host parses this
// output on every tool call; emitting a populated-looking envelope with no
// context, or invalid JSON, would be worse than firing.
func TestBuildToolReflectOutput_SilentOutputEncodesEmpty(t *testing.T) {
	out := buildToolReflectOutput(payloadWithResponseBytes(t, 10), 2048)

	encoded, err := json.Marshal(out)
	if err != nil {
		t.Fatalf("silent output does not encode: %v", err)
	}
	if string(encoded) != "{}" {
		t.Fatalf("silent output encoded as %s, want {}", encoded)
	}
}
