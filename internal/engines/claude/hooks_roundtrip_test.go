package claude

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/ctxloom/ctxloom/internal/core/engine"
	"github.com/ctxloom/ctxloom/internal/core/present"
	"github.com/ctxloom/ctxloom/internal/core/wire"
)

// TestHooks_ADeliveredPreToolHook_RoundTripsThroughTheCodec is the claude
// half of the hook ruling's probe. A unified pre_tool hook delivered through
// the hooks approach is registered in settings.json under the native event
// claude fires it on, and the payload claude writes for that event (its
// hook_event_name is the registration's key) decodes through Engine.Hooks()
// back to the unified event the hook was delivered as. Claude itself is not
// launched here: that the registered command runs is claude's contract, and
// this asserts the two halves ctxloom owns — the registration and the
// decode — agree on the event.
func TestHooks_ADeliveredPreToolHook_RoundTripsThroughTheCodec(t *testing.T) {
	eng, err := Build()
	require.NoError(t, err)
	def := eng.Root()
	start, project, _ := hostStart(t)
	const marker = "cat > /tmp/marker"
	hooks := wire.UnifiedHooks{PreTool: []wire.Hook{{Type: "command", Command: marker}}}
	_, err = def.Hooks.DeliverHooks(start, present.RootProjectRoot, engine.HooksInputs{Hooks: hooks}, nil)
	require.NoError(t, err)

	body, err := os.ReadFile(filepath.Join(project, ".claude", "settings.json"))
	require.NoError(t, err)
	var settings struct {
		Hooks map[string][]struct {
			Hooks []struct {
				Command string `json:"command"`
			} `json:"hooks"`
		} `json:"hooks"`
	}
	require.NoError(t, json.Unmarshal(body, &settings))
	var nativeEvent string
	for event, matchers := range settings.Hooks {
		for _, m := range matchers {
			for _, h := range m.Hooks {
				if h.Command == marker {
					nativeEvent = event
				}
			}
		}
	}
	require.Equal(t, hookEventPreToolUse, nativeEvent, "the delivered pre_tool hook is registered under the event claude fires before a tool")

	payload, err := json.Marshal(HookPayload{HookEventName: nativeEvent, SessionID: "s-1", ToolName: "Bash"})
	require.NoError(t, err)
	ev, err := eng.Hooks().Decode(nativeEvent, payload)
	require.NoError(t, err)
	require.Equal(t, engine.HookEvent{Event: "pre_tool", NativeSession: "s-1"}, ev)
}
